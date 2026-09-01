import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { inspectZip } from '../src/zip-inspector.js';

const crcTable = Array.from({ length: 256 }, (_, value) => {
  let crc = value;
  for (let bit = 0; bit < 8; bit += 1) crc = (crc & 1) ? (0xedb88320 ^ (crc >>> 1)) : (crc >>> 1);
  return crc >>> 0;
});

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) crc = crcTable[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function zipBuffer(files) {
  const localParts = [];
  const centralParts = [];
  let offset = 0;
  for (const [nameValue, contentValue] of Object.entries(files)) {
    const name = Buffer.from(nameValue, 'utf8');
    const content = Buffer.from(contentValue, 'utf8');
    const crc = crc32(content);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(content.length, 18);
    local.writeUInt32LE(content.length, 22);
    local.writeUInt16LE(name.length, 26);
    localParts.push(local, name, content);

    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(content.length, 20);
    central.writeUInt32LE(content.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(offset, 42);
    centralParts.push(central, name);
    offset += local.length + name.length + content.length;
  }

  const centralSize = centralParts.reduce((total, part) => total + part.length, 0);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(files).length, 8);
  end.writeUInt16LE(Object.keys(files).length, 10);
  end.writeUInt32LE(centralSize, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...localParts, ...centralParts, end]);
}

test('inspects a vehicle archive and reads embedded metadata', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'beamng-zip-'));
  try {
    const archive = join(directory, 'vehicle.zip');
    await writeFile(archive, zipBuffer({
      'vehicles/example/info.json': '{Name: "Example Coupe", Author: "Test Author", Version: "2.0"}',
      'vehicles/example/example.jbeam': '{}',
    }));
    const result = await inspectZip(archive);
    assert.equal(result.validArchive, true);
    assert.equal(result.category, 'Vehicles');
    assert.equal(result.metadata.title, 'Example Coupe');
    assert.equal(result.metadata.author, 'Test Author');
    assert.equal(result.issues.length, 0);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test('flags BeamNG content packaged one directory too deep', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'beamng-zip-'));
  try {
    const archive = join(directory, 'nested.zip');
    await writeFile(archive, zipBuffer({
      'wrapper/vehicles/example/info.json': '{Name: "Nested Coupe"}',
      'wrapper/vehicles/example/example.jbeam': '{}',
    }));
    const result = await inspectZip(archive);
    assert.equal(result.validArchive, true);
    assert.equal(result.wrapper, 'wrapper');
    assert.ok(result.issues.some((issue) => issue.code === 'nested-root' && issue.severity === 'warning'));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test('reports a non-ZIP file as an invalid archive', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'beamng-zip-'));
  try {
    const archive = join(directory, 'broken.zip');
    await writeFile(archive, 'not a zip archive');
    const result = await inspectZip(archive);
    assert.equal(result.validArchive, false);
    assert.equal(result.issues[0].code, 'invalid-archive');
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
