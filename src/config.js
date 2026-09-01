import { readFile, mkdir } from 'node:fs/promises';
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

export const APP_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');

export async function loadConfig() {
  const file = resolve(APP_DIR, 'config.json');
  const parsed = JSON.parse(await readFile(file, 'utf8'));
  const config = {
    ...parsed,
    beamngRoot: resolve(parsed.beamngRoot),
    activeModsDir: resolve(parsed.activeModsDir),
    libraryDir: resolve(parsed.libraryDir),
    gameInstallDir: resolve(parsed.gameInstallDir),
    scanRoots: parsed.scanRoots.map((entry) => resolve(entry)),
    port: Number(parsed.port) || 32145,
    scanConcurrency: Math.max(1, Math.min(12, Number(parsed.scanConcurrency) || 4)),
  };

  config.catalogFile = resolve(config.libraryDir, 'catalog.json');
  config.operationsLog = resolve(config.libraryDir, 'operations.jsonl');
  config.disabledDir = resolve(config.libraryDir, 'disabled');

  await Promise.all([
    mkdir(config.libraryDir, { recursive: true }),
    mkdir(config.disabledDir, { recursive: true }),
  ]);

  return config;
}

export function pathKey(value) {
  return resolve(value).replaceAll('/', sep).toLocaleLowerCase('en-US');
}

export function isWithin(parent, candidate) {
  const rel = relative(resolve(parent), resolve(candidate));
  return rel === '' || (!rel.startsWith(`..${sep}`) && rel !== '..' && !isAbsolute(rel));
}

export function assertWithin(parent, candidate, label = 'path') {
  if (!isWithin(parent, candidate)) {
    throw new Error(`${label} escapes its allowed root: ${candidate}`);
  }
}

export function safeRelative(value) {
  if (typeof value !== 'string' || value.trim() === '' || isAbsolute(value)) {
    throw new Error('Expected a non-empty relative path');
  }

  const normalized = value.replaceAll('/', sep).replaceAll('\\', sep);
  const segments = normalized.split(sep);
  if (segments.some((segment) => segment === '..' || segment === '')) {
    throw new Error(`Unsafe relative path: ${value}`);
  }
  return segments.join(sep);
}

export function relativeForDisplay(parent, child) {
  return relative(resolve(parent), resolve(child)).split(sep).join('/');
}
