import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { emptyCatalog, saveCatalog } from '../src/catalog.js';
import { diagnoseRuntime } from '../src/runtime-diagnostics.js';

test('maps a runtime JSON error to the uniquely matching previously enabled mod', async () => {
  const root = await mkdtemp(join(tmpdir(), 'beamng-runtime-'));
  try {
    const activeModsDir = join(root, 'current', 'mods');
    const libraryDir = join(root, 'library');
    const logs = join(root, 'old-user-data');
    const config = {
      beamngRoot: root,
      activeModsDir,
      libraryDir,
      catalogFile: join(libraryDir, 'catalog.json'),
    };
    await Promise.all([mkdir(libraryDir, { recursive: true }), mkdir(logs, { recursive: true })]);
    const catalog = emptyCatalog();
    catalog.mods = [{
      id: '22222222-2222-4222-8222-222222222222',
      title: 'Runtime Test Car',
      filename: 'runtime-test.zip',
      path: join(libraryDir, 'runtime-test.zip'),
      originalPath: join(activeModsDir, 'runtime-test.zip'),
      fingerprint: 'fingerprint',
      namespaces: { vehicles: ['runtime_test'], levels: [], ui: [] },
      issues: [],
      runtimeIssues: [],
      problematic: false,
      health: 'healthy',
      missing: false,
      enabled: false,
      source: 'third-party',
      size: 1,
    }];
    await saveCatalog(config, catalog);
    await writeFile(join(logs, 'beamng.log'), ' 1.000|E|json| unable to decode JSON: /vehicles/runtime_test/info.json\n');

    const result = await diagnoseRuntime(config, [logs]);
    const mod = result.catalog.mods[0];
    assert.equal(mod.health, 'needs-review');
    assert.equal(mod.runtimeIssues.length, 1);
    assert.equal(mod.runtimeIssues[0].code, 'runtime-json-errors');
    assert.equal(mod.runtimeIssues[0].severity, 'warning');
    assert.equal(result.summary.errorLines, 1);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
