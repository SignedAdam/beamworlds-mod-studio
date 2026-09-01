import { randomUUID } from 'node:crypto';
import { readdir, readFile, stat } from 'node:fs/promises';
import { basename, join, relative, resolve, sep } from 'node:path';
import { APP_DIR, isWithin, pathKey } from './config.js';
import { loadCatalog, saveCatalog } from './catalog.js';
import { inspectZip } from './zip-inspector.js';

const SKIP_DIRECTORY_NAMES = new Set(['.git', 'node_modules', '$recycle.bin', 'system volume information']);

async function discoverArchives(config) {
  const results = [];
  const visited = new Set();
  const exactSkips = [
    APP_DIR,
    resolve(config.beamngRoot, 'current', 'temp'),
    resolve(config.beamngRoot, 'Backups'),
  ].map(pathKey);

  for (const root of config.scanRoots) {
    const stack = [resolve(root)];
    while (stack.length > 0) {
      const current = stack.pop();
      const currentKey = pathKey(current);
      if (visited.has(currentKey) || exactSkips.some((skip) => currentKey === skip || currentKey.startsWith(`${skip}${sep}`))) continue;
      visited.add(currentKey);

      let entries;
      try {
        entries = await readdir(current, { withFileTypes: true });
      } catch (error) {
        if (['EACCES', 'EPERM', 'ENOENT'].includes(error.code)) continue;
        throw error;
      }

      for (const entry of entries) {
        const full = join(current, entry.name);
        if (entry.isDirectory()) {
          if (!SKIP_DIRECTORY_NAMES.has(entry.name.toLowerCase())) stack.push(full);
        } else if (entry.isFile() && /\.zip(?:\.stop)?$/i.test(entry.name)) {
          results.push(resolve(full));
        }
      }
    }
  }

  return [...new Map(results.map((path) => [pathKey(path), path])).values()]
    .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }));
}

async function readBeamDatabase(config) {
  const databaseFile = resolve(config.activeModsDir, 'db.json');
  let parsed;
  try {
    parsed = JSON.parse(await readFile(databaseFile, 'utf8'));
  } catch (error) {
    if (error.code === 'ENOENT') return { byPath: new Map(), byFilename: new Map() };
    throw new Error(`Cannot read BeamNG mod database: ${error.message}`);
  }

  const byPath = new Map();
  const byFilename = new Map();
  for (const [databaseKey, entry] of Object.entries(parsed.mods ?? {})) {
    if (!entry || typeof entry !== 'object' || !entry.filename) continue;
    const relativePath = String(entry.fullpath ?? '').replace(/^[/\\]*mods[/\\]*/i, '').replaceAll('/', sep);
    const absolutePath = resolve(config.activeModsDir, relativePath || entry.filename);
    const data = entry.modData ?? {};
    const normalized = {
      databaseKey,
      active: entry.active !== false,
      modId: entry.modID ?? null,
      modName: entry.modname ?? null,
      modType: entry.modType ?? null,
      infoPath: entry.modInfoPath ?? null,
      title: data.title ?? null,
      description: data.tag_line ?? data.message ?? null,
      author: data.username ?? null,
      version: data.version_string ?? null,
      resourceId: data.resource_id ?? null,
      resourceVersionId: data.resource_version_id ?? data.current_version_id ?? null,
      repositoryPath: data.path ?? null,
      via: data.via ?? null,
      source: String(entry.dirname ?? '').toLowerCase().includes('/repo') ? 'repository' : null,
    };
    byPath.set(pathKey(absolutePath), normalized);
    const filenameKey = entry.filename.toLocaleLowerCase('en-US');
    if (!byFilename.has(filenameKey)) byFilename.set(filenameKey, []);
    byFilename.get(filenameKey).push(normalized);
  }
  return { byPath, byFilename };
}

function databaseMatch(database, path) {
  const exact = database.byPath.get(pathKey(path));
  if (exact) return exact;
  const candidates = database.byFilename.get(basename(path).toLocaleLowerCase('en-US')) ?? [];
  return candidates.length === 1 ? candidates[0] : null;
}

function prettifyFilename(filename) {
  return filename
    .replace(/\.zip$/i, '')
    .replace(/(?:\s*&?\s*)?tg_?@?m0dsbeamng/ig, '')
    .replaceAll('_', ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function usefulTitle(value) {
  if (typeof value !== 'string') return null;
  const title = value.trim();
  if (!title || title.length > 200 || /^(?:car|truck|vehicle|map|level|unknown)$/i.test(title)) return null;
  return title;
}

function sourceFor(config, path, databaseEntry, existing) {
  if (existing?.source) return existing.source;
  const repoDir = resolve(config.activeModsDir, 'repo');
  if (databaseEntry?.source === 'repository' || isWithin(repoDir, path)) return 'repository';
  return 'third-party';
}

function locationFor(config, path) {
  if (isWithin(config.activeModsDir, path)) return 'enabled';
  if (isWithin(config.libraryDir, path)) return 'library';
  return 'loose';
}

function defaultActivePath(config, path, source, existing) {
  if (isWithin(config.activeModsDir, path)) return relative(config.activeModsDir, path);
  if (existing?.activeRelativePath) return existing.activeRelativePath.replaceAll('/', sep);
  return join(source === 'repository' ? 'repo' : '_managed', basename(path));
}

function preservedFields(existing) {
  return {
    tags: Array.isArray(existing?.tags) ? existing.tags : [],
    description: typeof existing?.description === 'string' ? existing.description : '',
    notes: typeof existing?.notes === 'string' ? existing.notes : '',
    problematic: existing?.problematic === true,
    categoryOverride: typeof existing?.categoryOverride === 'string' ? existing.categoryOverride : null,
    runtimeIssues: Array.isArray(existing?.runtimeIssues) ? existing.runtimeIssues : [],
  };
}

function summarizeHealth(item) {
  if (item.problematic) return 'problematic';
  const issues = [...item.issues, ...(item.runtimeIssues ?? [])];
  if (issues.some((issue) => issue.severity === 'error')) return 'invalid';
  if (issues.some((issue) => issue.severity === 'warning')) return 'needs-review';
  return 'healthy';
}

function compactDatabase(entry) {
  if (!entry) return null;
  return {
    databaseKey: entry.databaseKey,
    active: entry.active,
    modId: entry.modId,
    modName: entry.modName,
    modType: entry.modType,
    infoPath: entry.infoPath,
    title: entry.title,
    description: entry.description,
    author: entry.author,
    version: entry.version,
    resourceId: entry.resourceId,
    resourceVersionId: entry.resourceVersionId,
    repositoryPath: entry.repositoryPath,
    via: entry.via,
  };
}

function existingMatcher(catalog) {
  const byPath = new Map(catalog.mods.map((mod) => [pathKey(mod.path), mod]));
  const byFingerprint = new Map();
  for (const mod of catalog.mods) {
    if (!mod.fingerprint) continue;
    if (!byFingerprint.has(mod.fingerprint)) byFingerprint.set(mod.fingerprint, []);
    byFingerprint.get(mod.fingerprint).push(mod);
  }
  const claimed = new Set();

  return (path, fingerprint) => {
    const exact = byPath.get(pathKey(path));
    if (exact && !claimed.has(exact.id)) {
      claimed.add(exact.id);
      return exact;
    }
    const candidates = (byFingerprint.get(fingerprint) ?? []).filter((mod) => !claimed.has(mod.id));
    if (candidates.length === 1) {
      claimed.add(candidates[0].id);
      return candidates[0];
    }
    return null;
  };
}

export async function scanLibrary(config, options = {}) {
  const startedAt = new Date().toISOString();
  const [paths, database, catalog] = await Promise.all([
    discoverArchives(config),
    readBeamDatabase(config),
    loadCatalog(config),
  ]);
  const matchExisting = existingMatcher(catalog);
  const scanned = new Array(paths.length);
  let cursor = 0;
  let completed = 0;

  options.onProgress?.({ phase: 'inspect', completed: 0, total: paths.length, current: null });
  const worker = async () => {
    while (true) {
      const index = cursor;
      cursor += 1;
      if (index >= paths.length) return;
      const path = paths[index];
      const fileStat = await stat(path);
      const databaseEntry = databaseMatch(database, path);
      const inspection = await inspectZip(path, { databaseType: databaseEntry?.modType });
      const existing = matchExisting(path, inspection.fingerprint);
      const source = sourceFor(config, path, databaseEntry, existing);
      const fields = preservedFields(existing);
      const archiveDescription = usefulTitle(inspection.metadata.description) ?? databaseEntry?.description ?? existing?.archiveDescription ?? null;
      const item = {
        id: existing?.id ?? randomUUID(),
        path,
        filename: basename(path),
        originalPath: existing?.originalPath ?? path,
        location: locationFor(config, path),
        enabled: isWithin(config.activeModsDir, path),
        missing: false,
        source,
        activeRelativePath: defaultActivePath(config, path, source, existing),
        size: fileStat.size,
        modifiedAt: fileStat.mtime.toISOString(),
        fingerprint: inspection.fingerprint,
        validArchive: inspection.validArchive,
        entryCount: inspection.entryCount,
        compressedBytes: inspection.compressedBytes,
        uncompressedBytes: inspection.uncompressedBytes,
        wrapper: inspection.wrapper ?? null,
        autoCategory: databaseEntry ? inspection.category : (existing?.autoCategory ?? inspection.category),
        contentTags: databaseEntry ? inspection.contentTags : (existing?.contentTags ?? inspection.contentTags),
        namespaces: inspection.namespaces,
        nestedArchiveCount: inspection.nestedArchiveCount ?? 0,
        title: usefulTitle(databaseEntry?.title) ?? usefulTitle(existing?.title) ?? usefulTitle(inspection.metadata.title) ?? prettifyFilename(basename(path)),
        archiveDescription,
        author: usefulTitle(databaseEntry?.author) ?? usefulTitle(existing?.author) ?? usefulTitle(inspection.metadata.author),
        version: usefulTitle(databaseEntry?.version) ?? usefulTitle(existing?.version) ?? usefulTitle(inspection.metadata.version),
        metadataDocuments: inspection.metadataDocuments.length > 0 ? inspection.metadataDocuments : (existing?.metadataDocuments ?? []),
        previewPath: inspection.previewPath ?? existing?.previewPath ?? null,
        database: compactDatabase(databaseEntry) ?? existing?.database ?? null,
        issues: inspection.issues,
        ...fields,
      };
      const selfReport = [item.title, item.archiveDescription, item.database?.description].filter(Boolean).join(' ');
      if (/(?:temporarily|currently) broken|\bnot working\b|\bno longer works?\b|\bdeprecated\b|\bunsupported\b/i.test(selfReport)) {
        item.issues.push({
          code: 'self-reported-broken',
          severity: 'warning',
          message: 'The mod’s own title or description says it is broken, unsupported, or no longer working.',
        });
      }
      item.health = summarizeHealth(item);
      scanned[index] = item;
      completed += 1;
      options.onProgress?.({ phase: 'inspect', completed, total: paths.length, current: path });
    }
  };

  await Promise.all(Array.from({ length: Math.min(config.scanConcurrency, Math.max(1, paths.length)) }, () => worker()));

  const duplicateGroups = new Map();
  for (const item of scanned) {
    if (!item.fingerprint) continue;
    if (!duplicateGroups.has(item.fingerprint)) duplicateGroups.set(item.fingerprint, []);
    duplicateGroups.get(item.fingerprint).push(item);
  }
  for (const group of duplicateGroups.values()) {
    if (group.length < 2) continue;
    for (const item of group) {
      item.issues.push({
        code: 'duplicate-content',
        severity: 'info',
        message: `${group.length} archives contain the same packaged files.`,
        relatedIds: group.filter((candidate) => candidate.id !== item.id).map((candidate) => candidate.id),
      });
    }
  }

  const seenIds = new Set(scanned.map((item) => item.id));
  const missing = catalog.mods
    .filter((item) => !seenIds.has(item.id))
    .map((item) => ({ ...item, enabled: false, missing: true, location: 'missing', health: item.problematic ? 'problematic' : 'missing' }));
  catalog.mods = [...scanned, ...missing].sort((a, b) => a.title.localeCompare(b.title, undefined, { sensitivity: 'base' }));
  catalog.lastScan = {
    startedAt,
    finishedAt: new Date().toISOString(),
    discovered: scanned.length,
    missing: missing.length,
  };
  await saveCatalog(config, catalog);
  options.onProgress?.({ phase: 'done', completed: paths.length, total: paths.length, current: null });
  return catalog;
}

export function catalogStats(catalog) {
  const present = catalog.mods.filter((mod) => !mod.missing);
  return {
    total: present.length,
    enabled: present.filter((mod) => mod.enabled).length,
    disabled: present.filter((mod) => !mod.enabled).length,
    repository: present.filter((mod) => mod.source === 'repository').length,
    thirdParty: present.filter((mod) => mod.source === 'third-party').length,
    problematic: present.filter((mod) => mod.health === 'problematic').length,
    invalid: present.filter((mod) => mod.health === 'invalid').length,
    needsReview: present.filter((mod) => mod.health === 'needs-review').length,
    duplicates: present.filter((mod) => mod.issues.some((issue) => issue.code === 'duplicate-content')).length,
    loose: present.filter((mod) => mod.location === 'loose').length,
    bytes: present.reduce((total, mod) => total + mod.size, 0),
  };
}
