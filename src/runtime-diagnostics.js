import { readdir, readFile } from 'node:fs/promises';
import { basename, join, resolve } from 'node:path';
import { isWithin } from './config.js';
import { loadCatalog, saveCatalog } from './catalog.js';

function healthFor(mod) {
  if (mod.problematic) return 'problematic';
  const issues = [...(mod.issues ?? []), ...(mod.runtimeIssues ?? [])];
  if (issues.some((issue) => issue.severity === 'error')) return 'invalid';
  if (issues.some((issue) => issue.severity === 'warning')) return 'needs-review';
  return 'healthy';
}

async function findLogs(directories) {
  const paths = [];
  for (const directory of directories.map((value) => resolve(value))) {
    let entries;
    try {
      entries = await readdir(directory, { withFileTypes: true });
    } catch (error) {
      if (error.code === 'ENOENT') continue;
      throw error;
    }
    for (const entry of entries) {
      if (entry.isFile() && /^beamng(?:\.\d+)?\.log$/i.test(entry.name)) paths.push(join(directory, entry.name));
    }
  }
  return paths.sort((a, b) => a.localeCompare(b));
}

function classifyMessage(message) {
  if (/unable to (?:decode|deserialize|read info)|invalid json|json (?:decode|parsing|parse).*error/i.test(message)) return 'json';
  if (/duplicate part found|parts names are duplicate/i.test(message)) return 'duplicate';
  if (/failed to (?:create resource|load)|missing source texture|texture missing|404 - not found|no valid data found/i.test(message)) return 'missing';
  return 'other';
}

function originallyEnabled(config, mod) {
  return typeof mod.originalPath === 'string' && isWithin(config.activeModsDir, mod.originalPath);
}

function candidateIndex(catalog) {
  const index = new Map();
  for (const mod of catalog.mods) {
    for (const [kind, namespaces] of Object.entries(mod.namespaces ?? {})) {
      if (!['vehicles', 'levels'].includes(kind)) continue;
      for (const namespace of namespaces ?? []) {
        const key = `${kind}/${String(namespace).toLocaleLowerCase('en-US')}`;
        if (!index.has(key)) index.set(key, []);
        index.get(key).push(mod);
      }
    }
  }
  return index;
}

function selectCandidates(config, candidates) {
  const enabled = candidates.filter((mod) => originallyEnabled(config, mod));
  const pool = enabled.length > 0 ? enabled : candidates;
  const fingerprints = new Set(pool.map((mod) => mod.fingerprint).filter(Boolean));
  if (pool.length === 1 || fingerprints.size === 1) return { mods: pool, wasEnabled: enabled.length > 0 };
  return { mods: [], wasEnabled: false };
}

export async function diagnoseRuntime(config, directories = [resolve(config.beamngRoot, 'current')]) {
  const [catalog, logs] = await Promise.all([loadCatalog(config), findLogs(directories)]);
  if (logs.length === 0) throw new Error('No BeamNG runtime logs were found. Launch the clean game once, then analyze again.');
  const references = new Map();
  let errorLines = 0;

  for (const path of logs) {
    const text = await readFile(path, 'utf8');
    for (const line of text.split(/\r?\n/)) {
      if (!line.includes('|E|')) continue;
      errorLines += 1;
      const message = line.replace(/^.*?\|E\|[^|]*\|\s*/, '').trim();
      const keys = new Set([...message.matchAll(/\/(vehicles|levels)\/([^/\s'"|{}]+)/ig)]
        .map((match) => `${match[1].toLocaleLowerCase('en-US')}/${match[2].toLocaleLowerCase('en-US')}`));
      for (const key of keys) {
        if (!references.has(key)) {
          references.set(key, { total: 0, json: 0, duplicate: 0, missing: 0, other: 0, logs: new Set(), examples: [] });
        }
        const record = references.get(key);
        const kind = classifyMessage(message);
        record.total += 1;
        record[kind] += 1;
        record.logs.add(basename(path));
        if (record.examples.length < 3 && !record.examples.includes(message)) record.examples.push(message);
      }
    }
  }

  for (const mod of catalog.mods) mod.runtimeIssues = [];
  const index = candidateIndex(catalog);
  let matchedNamespaces = 0;
  let ambiguousNamespaces = 0;

  for (const [namespace, record] of references) {
    const candidates = index.get(namespace) ?? [];
    if (candidates.length === 0) continue;
    const selected = selectCandidates(config, candidates);
    if (selected.mods.length === 0) {
      ambiguousNamespaces += 1;
      continue;
    }

    const actionable = record.json > 0 || record.duplicate >= 20 || record.missing >= 10 || record.total >= 100;
    const severity = selected.wasEnabled && actionable ? 'warning' : 'info';
    const dominant = [
      ['runtime-json-errors', record.json, 'JSON/content parsing errors'],
      ['runtime-duplicate-parts', record.duplicate, 'duplicate part errors'],
      ['runtime-missing-assets', record.missing, 'missing asset errors'],
      ['runtime-errors', record.other, 'other runtime errors'],
    ].sort((a, b) => b[1] - a[1])[0];
    const issue = {
      code: dominant[0],
      severity,
      message: `${record.total.toLocaleString('en-US')} historical BeamNG errors referenced ${namespace}; ${dominant[1].toLocaleString('en-US')} were ${dominant[2].toLowerCase()}.`,
      namespace,
      count: record.total,
      logs: [...record.logs].sort(),
      examples: record.examples,
    };
    for (const mod of selected.mods) mod.runtimeIssues.push(issue);
    matchedNamespaces += 1;
  }

  for (const mod of catalog.mods) {
    mod.runtimeIssues.sort((a, b) => (a.severity === b.severity ? b.count - a.count : a.severity === 'warning' ? -1 : 1));
    mod.runtimeIssues = mod.runtimeIssues.slice(0, 12);
    mod.health = healthFor(mod);
  }
  catalog.lastDiagnostics = {
    at: new Date().toISOString(),
    logs: logs.map((path) => basename(path)),
    errorLines,
    referencedNamespaces: references.size,
    matchedNamespaces,
    ambiguousNamespaces,
  };
  await saveCatalog(config, catalog);
  return { catalog, summary: catalog.lastDiagnostics };
}
