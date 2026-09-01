import { appendFile, copyFile, open, readFile, rename } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';

export const CATALOG_SCHEMA_VERSION = 1;

export function emptyCatalog() {
  return {
    schemaVersion: CATALOG_SCHEMA_VERSION,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    lastScan: null,
    mods: [],
  };
}

export async function loadCatalog(config) {
  let text;
  try {
    text = await readFile(config.catalogFile, 'utf8');
  } catch (error) {
    if (error.code === 'ENOENT') return emptyCatalog();
    throw error;
  }

  let catalog;
  try {
    catalog = JSON.parse(text);
  } catch (error) {
    throw new Error(`Catalog is not valid JSON (${config.catalogFile}): ${error.message}`);
  }

  if (catalog.schemaVersion !== CATALOG_SCHEMA_VERSION || !Array.isArray(catalog.mods)) {
    throw new Error(`Unsupported catalog schema in ${config.catalogFile}`);
  }
  return catalog;
}

export async function saveCatalog(config, catalog) {
  catalog.updatedAt = new Date().toISOString();
  const temporary = `${config.catalogFile}.${process.pid}.${randomUUID()}.tmp`;
  const payload = `${JSON.stringify(catalog, null, 2)}\n`;
  const handle = await open(temporary, 'wx');

  try {
    await handle.writeFile(payload, 'utf8');
    await handle.sync();
  } finally {
    await handle.close();
  }

  try {
    await copyFile(config.catalogFile, `${config.catalogFile}.bak`);
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }

  await rename(temporary, config.catalogFile);
}

export async function logOperation(config, operation) {
  const entry = {
    at: new Date().toISOString(),
    ...operation,
  };
  await appendFile(config.operationsLog, `${JSON.stringify(entry)}\n`, 'utf8');
}

export function editablePatch(input) {
  const patch = {};

  if (Object.hasOwn(input, 'description')) {
    if (typeof input.description !== 'string' || input.description.length > 20_000) {
      throw new Error('Description must be text no longer than 20,000 characters');
    }
    patch.description = input.description.trim();
  }

  if (Object.hasOwn(input, 'notes')) {
    if (typeof input.notes !== 'string' || input.notes.length > 20_000) {
      throw new Error('Notes must be text no longer than 20,000 characters');
    }
    patch.notes = input.notes.trim();
  }

  if (Object.hasOwn(input, 'tags')) {
    if (!Array.isArray(input.tags) || input.tags.length > 50) {
      throw new Error('Tags must be an array containing at most 50 values');
    }
    patch.tags = [...new Set(input.tags.map((tag) => {
      if (typeof tag !== 'string' || tag.length > 60) throw new Error('Each tag must be at most 60 characters');
      return tag.trim();
    }).filter(Boolean))].sort((a, b) => a.localeCompare(b));
  }

  if (Object.hasOwn(input, 'problematic')) {
    if (typeof input.problematic !== 'boolean') throw new Error('Problematic must be true or false');
    patch.problematic = input.problematic;
  }

  if (Object.hasOwn(input, 'categoryOverride')) {
    if (input.categoryOverride !== null && (typeof input.categoryOverride !== 'string' || input.categoryOverride.length > 80)) {
      throw new Error('Category override must be null or text no longer than 80 characters');
    }
    patch.categoryOverride = input.categoryOverride?.trim() || null;
  }

  return patch;
}
