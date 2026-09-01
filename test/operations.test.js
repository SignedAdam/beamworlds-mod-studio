import test from 'node:test';
import assert from 'node:assert/strict';
import { access, mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { emptyCatalog, loadCatalog, saveCatalog } from '../src/catalog.js';
import { setEnabled } from '../src/operations.js';

async function exists(path) {
  try { await access(path); return true; } catch (error) {
    if (error.code === 'ENOENT') return false;
    throw error;
  }
}

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), 'beamng-ops-'));
  const activeModsDir = join(root, 'current', 'mods');
  const libraryDir = join(root, 'library');
  const config = {
    beamngRoot: root,
    activeModsDir,
    libraryDir,
    disabledDir: join(libraryDir, 'disabled'),
    catalogFile: join(libraryDir, 'catalog.json'),
    operationsLog: join(libraryDir, 'operations.jsonl'),
  };
  await Promise.all([mkdir(activeModsDir, { recursive: true }), mkdir(config.disabledDir, { recursive: true })]);
  return { root, config };
}

function modRecord(overrides = {}) {
  return {
    id: '11111111-1111-4111-8111-111111111111',
    title: 'Test Mod',
    filename: 'test.zip',
    path: '',
    originalPath: '',
    source: 'third-party',
    activeRelativePath: '_managed/test.zip',
    enabled: true,
    missing: false,
    location: 'enabled',
    fingerprint: null,
    size: 7,
    issues: [],
    health: 'healthy',
    tags: [],
    description: '',
    notes: '',
    problematic: false,
    categoryOverride: null,
    ...overrides,
  };
}

test('disable and enable are reversible moves with catalog updates', async () => {
  const { root, config } = await fixture();
  try {
    const source = resolve(config.activeModsDir, '_managed', 'test.zip');
    await mkdir(join(config.activeModsDir, '_managed'), { recursive: true });
    await writeFile(source, 'payload');
    const mod = modRecord({ path: source, originalPath: source });
    const catalog = emptyCatalog();
    catalog.mods = [mod];
    await saveCatalog(config, catalog);

    await setEnabled(config, mod.id, false);
    const disabledCatalog = await loadCatalog(config);
    const disabled = disabledCatalog.mods[0];
    assert.equal(disabled.enabled, false);
    assert.equal(disabled.location, 'library');
    assert.equal(await exists(source), false);
    assert.equal(await readFile(disabled.path, 'utf8'), 'payload');

    await setEnabled(config, mod.id, true);
    const enabledCatalog = await loadCatalog(config);
    const enabled = enabledCatalog.mods[0];
    assert.equal(enabled.enabled, true);
    assert.equal(enabled.location, 'enabled');
    assert.equal(await readFile(source, 'utf8'), 'payload');
    assert.equal(await exists(disabled.path), false);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('enabling never overwrites an existing archive', async () => {
  const { root, config } = await fixture();
  try {
    const disabledPath = join(config.disabledDir, 'third-party', 'source.zip');
    const occupiedPath = join(config.activeModsDir, '_managed', 'same.zip');
    await Promise.all([mkdir(join(config.activeModsDir, '_managed'), { recursive: true }), mkdir(join(config.disabledDir, 'third-party'), { recursive: true })]);
    await writeFile(disabledPath, 'new mod');
    await writeFile(occupiedPath, 'existing mod');
    const mod = modRecord({
      path: disabledPath,
      filename: 'same.zip',
      originalPath: disabledPath,
      activeRelativePath: '_managed/same.zip',
      enabled: false,
      location: 'library',
    });
    const catalog = emptyCatalog();
    catalog.mods = [mod];
    await saveCatalog(config, catalog);

    await setEnabled(config, mod.id, true);
    const updated = (await loadCatalog(config)).mods[0];
    assert.equal(await readFile(occupiedPath, 'utf8'), 'existing mod');
    assert.equal(await readFile(updated.path, 'utf8'), 'new mod');
    assert.notEqual(updated.path.toLocaleLowerCase(), occupiedPath.toLocaleLowerCase());
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('an unsafe active path is rejected without moving the archive', async () => {
  const { root, config } = await fixture();
  try {
    const disabledPath = join(config.disabledDir, 'third-party', 'source.zip');
    await mkdir(join(config.disabledDir, 'third-party'), { recursive: true });
    await writeFile(disabledPath, 'payload');
    const mod = modRecord({
      path: disabledPath,
      originalPath: disabledPath,
      activeRelativePath: '../escape.zip',
      enabled: false,
      location: 'library',
    });
    const catalog = emptyCatalog();
    catalog.mods = [mod];
    await saveCatalog(config, catalog);

    await assert.rejects(setEnabled(config, mod.id, true), /Unsafe relative path/);
    assert.equal(await readFile(disabledPath, 'utf8'), 'payload');
    assert.equal(await exists(join(root, 'current', 'escape.zip')), false);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test('a BeamNG .zip.stop archive regains its ZIP extension when enabled', async () => {
  const { root, config } = await fixture();
  try {
    const disabledPath = join(config.disabledDir, 'third-party', 'stopped.zip.stop');
    await mkdir(join(config.disabledDir, 'third-party'), { recursive: true });
    await writeFile(disabledPath, 'payload');
    const mod = modRecord({
      path: disabledPath,
      filename: 'stopped.zip.stop',
      originalPath: disabledPath,
      activeRelativePath: '_managed/stopped.zip.stop',
      enabled: false,
      location: 'library',
    });
    const catalog = emptyCatalog();
    catalog.mods = [mod];
    await saveCatalog(config, catalog);

    await setEnabled(config, mod.id, true);
    const enabled = (await loadCatalog(config)).mods[0];
    assert.equal(enabled.path, join(config.activeModsDir, '_managed', 'stopped.zip'));
    assert.equal(await readFile(enabled.path, 'utf8'), 'payload');
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
