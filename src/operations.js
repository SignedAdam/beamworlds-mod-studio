import { constants } from 'node:fs';
import { access, copyFile, mkdir, rename, stat, unlink } from 'node:fs/promises';
import { basename, dirname, extname, join, parse, relative, resolve } from 'node:path';
import { editablePatch, loadCatalog, logOperation, saveCatalog } from './catalog.js';
import { assertWithin, isWithin, safeRelative } from './config.js';
import { inspectZip } from './zip-inspector.js';

let operationTail = Promise.resolve();

function serializeOperation(callback) {
  const result = operationTail.then(callback, callback);
  operationTail = result.catch(() => {});
  return result;
}

async function exists(path) {
  try {
    await access(path);
    return true;
  } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw error;
  }
}

async function uniqueDestination(preferred, id) {
  if (!await exists(preferred)) return preferred;
  const parts = parse(preferred);
  const shortId = id.replaceAll('-', '').slice(0, 8);
  let counter = 0;
  while (true) {
    const suffix = counter === 0 ? ` [${shortId}]` : ` [${shortId}-${counter + 1}]`;
    const candidate = join(parts.dir, `${parts.name}${suffix}${parts.ext}`);
    if (!await exists(candidate)) return candidate;
    counter += 1;
  }
}

async function moveArchive(source, preferredDestination, expectedFingerprint, modId) {
  const destination = await uniqueDestination(preferredDestination, modId);
  await mkdir(dirname(destination), { recursive: true });

  try {
    await rename(source, destination);
    return destination;
  } catch (error) {
    if (error.code !== 'EXDEV') throw error;
  }

  try {
    await copyFile(source, destination, constants.COPYFILE_EXCL);
    const [sourceStat, destinationStat] = await Promise.all([stat(source), stat(destination)]);
    if (sourceStat.size !== destinationStat.size) throw new Error('Copied archive size does not match its source');
    if (expectedFingerprint) {
      const copied = await inspectZip(destination);
      if (copied.fingerprint !== expectedFingerprint) throw new Error('Copied archive fingerprint does not match its source');
    }
    await unlink(source);
    return destination;
  } catch (error) {
    try { await unlink(destination); } catch (cleanupError) {
      if (cleanupError.code !== 'ENOENT') error.cleanupError = cleanupError.message;
    }
    throw error;
  }
}

function findMod(catalog, id) {
  const mod = catalog.mods.find((candidate) => candidate.id === id);
  if (!mod) throw new Error(`Unknown mod: ${id}`);
  if (mod.missing) throw new Error(`Mod file is missing: ${mod.filename}`);
  return mod;
}

function disabledDestination(config, mod) {
  const sourceFolder = mod.source === 'repository' ? 'repository' : 'third-party';
  const destination = resolve(config.disabledDir, sourceFolder, mod.id, basename(mod.path));
  assertWithin(config.disabledDir, destination, 'Disabled destination');
  return destination;
}

function enabledDestination(config, mod) {
  let rel = safeRelative(mod.activeRelativePath || join(mod.source === 'repository' ? 'repo' : '_managed', mod.filename));
  if (rel.toLowerCase().endsWith('.zip.stop')) rel = rel.slice(0, -5);
  const destination = resolve(config.activeModsDir, rel);
  assertWithin(config.activeModsDir, destination, 'Enabled destination');
  if (extname(destination).toLowerCase() !== '.zip') throw new Error('Only ZIP archives can be enabled');
  return destination;
}

async function applyMove(config, mod, action) {
  const from = mod.path;
  let preferred;
  if (action === 'enable') {
    if (mod.enabled && isWithin(config.activeModsDir, mod.path)) return null;
    preferred = enabledDestination(config, mod);
  } else {
    if (!mod.enabled && isWithin(config.libraryDir, mod.path)) return null;
    if (isWithin(config.activeModsDir, mod.path)) mod.activeRelativePath = relative(config.activeModsDir, mod.path);
    preferred = disabledDestination(config, mod);
  }

  const to = await moveArchive(from, preferred, mod.fingerprint, mod.id);
  mod.path = to;
  mod.filename = basename(to);
  mod.enabled = action === 'enable';
  mod.location = action === 'enable' ? 'enabled' : 'library';
  mod.missing = false;
  if (action === 'enable') mod.activeRelativePath = relative(config.activeModsDir, to);

  const operation = { action, modId: mod.id, title: mod.title, from, to };
  await logOperation(config, operation);
  return operation;
}

export function setEnabled(config, id, enabled) {
  return serializeOperation(async () => {
    const catalog = await loadCatalog(config);
    const mod = findMod(catalog, id);
    const operation = await applyMove(config, mod, enabled ? 'enable' : 'disable');
    if (operation) await saveCatalog(config, catalog);
    return { catalog, operation };
  });
}

export function setManyEnabled(config, ids, enabled) {
  return serializeOperation(async () => {
    if (!Array.isArray(ids) || ids.length > 5_000) throw new Error('Expected an array of mod IDs');
    const catalog = await loadCatalog(config);
    const operations = [];
    const errors = [];
    for (const id of [...new Set(ids)]) {
      try {
        const operation = await applyMove(config, findMod(catalog, id), enabled ? 'enable' : 'disable');
        if (operation) operations.push(operation);
      } catch (error) {
        errors.push({ id, message: error.message });
      }
    }
    if (operations.length > 0) await saveCatalog(config, catalog);
    return { catalog, operations, errors };
  });
}

export function preserveAll(config) {
  return serializeOperation(async () => {
    const catalog = await loadCatalog(config);
    const candidates = catalog.mods.filter((mod) => !mod.missing && mod.location !== 'library');
    const operations = [];
    const errors = [];
    for (const mod of candidates) {
      try {
        const operation = await applyMove(config, mod, 'disable');
        if (operation) operations.push(operation);
      } catch (error) {
        errors.push({ id: mod.id, path: mod.path, message: error.message });
      }
    }
    if (operations.length > 0) await saveCatalog(config, catalog);
    await logOperation(config, {
      action: 'preserve-all',
      moved: operations.length,
      failed: errors.length,
    });
    return { catalog, operations, errors };
  });
}

export function updateMod(config, id, input) {
  return serializeOperation(async () => {
    const catalog = await loadCatalog(config);
    const mod = findMod(catalog, id);
    const patch = editablePatch(input);
    Object.assign(mod, patch);
    const issues = [...(mod.issues ?? []), ...(mod.runtimeIssues ?? [])];
    mod.health = mod.problematic
      ? 'problematic'
      : issues.some((issue) => issue.severity === 'error')
        ? 'invalid'
        : issues.some((issue) => issue.severity === 'warning')
          ? 'needs-review'
          : 'healthy';
    await saveCatalog(config, catalog);
    await logOperation(config, { action: 'edit', modId: mod.id, fields: Object.keys(patch) });
    return { catalog, mod };
  });
}
