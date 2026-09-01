import { createHash } from 'node:crypto';
import JSON5 from 'json5';
import yauzl from 'yauzl';

const KNOWN_ROOTS = new Set([
  'art', 'campaigns', 'core', 'gameplay', 'levels', 'lua', 'mod_info', 'music',
  'scripts', 'settings', 'sound', 'tech', 'ui', 'vehicles',
]);
const ARCHIVE_EXTENSIONS = /\.(?:zip|7z|rar)$/i;
const IMAGE_EXTENSIONS = /\.(?:png|jpe?g|webp)$/i;
const MAX_METADATA_FILE = 256 * 1024;
const MAX_METADATA_TOTAL = 512 * 1024;

function openZip(path) {
  return new Promise((resolve, reject) => {
    yauzl.open(path, {
      autoClose: false,
      decodeStrings: true,
      lazyEntries: true,
      strictFileNames: false,
      validateEntrySizes: true,
    }, (error, zip) => error ? reject(error) : resolve(zip));
  });
}

function normalizeEntryName(value) {
  return value.replaceAll('\\', '/').replace(/^\.\//, '');
}

function metadataPriority(name) {
  const lower = name.toLowerCase();
  if (/^(?:[^/]+\/)?mod_info\/[^/]+\/info\.json$/.test(lower)) return 0;
  if (/^(?:[^/]+\/)?info\.json$/.test(lower)) return 1;
  if (/^(?:[^/]+\/)?vehicles\/[^/]+\/info\.json$/.test(lower)) return 2;
  if (/^(?:[^/]+\/)?levels\/[^/]+\/info\.json$/.test(lower)) return 3;
  return null;
}

function imagePriority(name) {
  const lower = name.toLowerCase();
  if (!IMAGE_EXTENSIONS.test(lower)) return null;
  if (/mod_info\/[^/]+\/(?:icon|logo|thumb|preview)/.test(lower)) return 0;
  if (/vehicles\/[^/]+\/(?:default|preview|logo)/.test(lower)) return 1;
  if (/levels\/[^/]+\/(?:preview|main|logo)/.test(lower)) return 2;
  return null;
}

function readEntry(zip, entry, limit) {
  return new Promise((resolve, reject) => {
    zip.openReadStream(entry, (error, stream) => {
      if (error) return reject(error);
      const chunks = [];
      let bytes = 0;
      stream.on('data', (chunk) => {
        bytes += chunk.length;
        if (bytes > limit) {
          stream.destroy(new Error(`Entry exceeds ${limit} bytes`));
          return;
        }
        chunks.push(chunk);
      });
      stream.once('error', reject);
      stream.once('end', () => resolve(Buffer.concat(chunks)));
    });
  });
}

function unwrapLogicalPaths(names) {
  const files = names.filter((name) => !name.endsWith('/'));
  const top = new Set(files.map((name) => name.split('/')[0].toLowerCase()));
  const directKnown = [...top].some((part) => KNOWN_ROOTS.has(part)) || files.some((name) => !name.includes('/') && name.toLowerCase() === 'info.json');
  if (directKnown || top.size !== 1) return { paths: files, wrapper: null };

  const [wrapper] = [...top];
  const prefixLength = wrapper.length + 1;
  const stripped = files
    .filter((name) => name.toLowerCase().startsWith(`${wrapper}/`))
    .map((name) => name.slice(prefixLength));
  const nestedTop = new Set(stripped.map((name) => name.split('/')[0].toLowerCase()));
  const nestedKnown = [...nestedTop].some((part) => KNOWN_ROOTS.has(part)) || stripped.some((name) => !name.includes('/') && name.toLowerCase() === 'info.json');
  return nestedKnown ? { paths: stripped, wrapper: files[0].split('/')[0] } : { paths: files, wrapper: null };
}

function inferContent(logicalPaths, archiveName, databaseType) {
  const lower = logicalPaths.map((name) => name.toLowerCase());
  const namespace = (rootName) => [...new Set(lower
    .filter((name) => name.startsWith(`${rootName}/`))
    .map((name) => name.split('/')[1])
    .filter(Boolean))].sort();
  const has = (pattern) => lower.some((name) => pattern.test(name));
  const nestedArchives = lower.filter((name) => ARCHIVE_EXTENSIONS.test(name));
  const categories = [];

  const hasLevels = has(/^levels\//);
  const hasVehicles = has(/^vehicles\//);
  const hasVehicleInfo = has(/^vehicles\/[^/]+\/info\.json$/);
  const hasWheels = has(/^vehicles\/(?:common\/)?(?:wheels?|tires?)\//) || has(/(?:^|\/)wheel[^/]*\.jbeam$/);
  const hasSkins = has(/(?:^|\/)(?:skins?|liveries)(?:\/|\.)/) || has(/\.skin\.materials\.json$/);
  const hasUi = has(/^ui\//) || has(/^lua\/(?:ge\/extensions\/)?ui\//) || has(/ui[_-]?modules?/);
  const hasGameplay = has(/^gameplay\//) || has(/^lua\//) || has(/^scripts\//);
  const hasScenario = has(/^gameplay\/(?:missions|scenarios)\//) || has(/(?:mission|scenario)[^/]*\.json$/);
  const hasSound = has(/^(?:art\/sound|sound|music)\//) || has(/\.(?:ogg|wav|mp3)$/);
  const hasProps = has(/^vehicles\/(?:common\/)?props?\//) || has(/^art\/shapes\/props?\//);

  if (hasLevels || databaseType === 'map') categories.push('Maps');
  if (hasVehicleInfo || databaseType === 'vehicle') categories.push('Vehicles');
  if (hasWheels) categories.push('Wheels & tires');
  if (hasSkins) categories.push('Skins & liveries');
  if (hasProps) categories.push('Props');
  if (hasScenario) categories.push('Scenarios');
  if (hasUi) categories.push('UI & apps');
  if (hasGameplay && !hasUi && !hasScenario) categories.push('Gameplay & utilities');
  if (hasSound && !hasVehicles) categories.push('Sounds');
  if (hasVehicles && !hasVehicleInfo && !hasWheels && !hasSkins && databaseType !== 'vehicle') categories.push('Vehicle parts');
  if (nestedArchives.length > 0 && categories.length === 0) categories.push('Mod packs');

  if (categories.length === 0) {
    const filename = archiveName.toLowerCase();
    if (/map|road|highway|autobahn|raceway|speedway|island|valley|canyon|route|rutas/.test(filename)) categories.push('Maps');
    else if (/wheel|rim|tire|tyre/.test(filename)) categories.push('Wheels & tires');
    else if (/skin|liver/.test(filename)) categories.push('Skins & liveries');
    else if (/career|traffic|camera|repair|resolver|manager|dispatch|randomizer/.test(filename)) categories.push('Gameplay & utilities');
    else categories.push('Other');
  }

  return {
    category: categories[0],
    contentTags: categories,
    namespaces: {
      levels: namespace('levels'),
      vehicles: namespace('vehicles'),
      ui: namespace('ui'),
    },
    nestedArchiveCount: nestedArchives.length,
  };
}

function findString(object, keys) {
  if (!object || typeof object !== 'object' || Array.isArray(object)) return null;
  const entries = Object.entries(object);
  for (const key of keys) {
    const found = entries.find(([candidate]) => candidate.toLowerCase() === key.toLowerCase());
    if (found && (typeof found[1] === 'string' || typeof found[1] === 'number')) {
      const value = String(found[1]).trim();
      if (value) return value;
    }
  }
  return null;
}

function normalizedMetadata(documents) {
  const objects = documents.map((document) => document.data).filter(Boolean);
  const pick = (keys) => objects.map((object) => findString(object, keys)).find(Boolean) ?? null;
  return {
    title: pick(['title', 'name', 'displayName']),
    author: pick(['author', 'authors', 'creator', 'username']),
    version: pick(['version', 'version_string', 'modVersion']),
    description: pick(['description', 'tag_line', 'tagline', 'summary']),
  };
}

export async function inspectZip(path, options = {}) {
  const issues = [];
  let zip;
  try {
    zip = await openZip(path);
  } catch (error) {
    return {
      validArchive: false,
      fingerprint: null,
      entryCount: 0,
      compressedBytes: 0,
      uncompressedBytes: 0,
      category: 'Invalid archive',
      contentTags: [],
      namespaces: { levels: [], vehicles: [], ui: [] },
      metadata: {},
      metadataDocuments: [],
      previewPath: null,
      issues: [{ code: 'invalid-archive', severity: 'error', message: error.message }],
    };
  }

  const hash = createHash('sha256');
  const names = [];
  const metadataEntries = [];
  const previewEntries = [];
  let entryCount = 0;
  let compressedBytes = 0;
  let uncompressedBytes = 0;
  let encryptedEntries = 0;
  let unsupportedEntries = 0;

  try {
    await new Promise((resolve, reject) => {
      let settled = false;
      const fail = (error) => {
        if (settled) return;
        settled = true;
        reject(error);
      };

      zip.once('error', fail);
      zip.on('entry', (entry) => {
        const name = normalizeEntryName(entry.fileName);
        names.push(name);
        entryCount += 1;
        compressedBytes += entry.compressedSize;
        uncompressedBytes += entry.uncompressedSize;
        hash.update(name, 'utf8');
        hash.update(`\0${entry.crc32}\0${entry.compressionMethod}\0${entry.compressedSize}\0${entry.uncompressedSize}\n`);

        if ((entry.generalPurposeBitFlag & 0x1) !== 0) encryptedEntries += 1;
        if (![0, 8].includes(entry.compressionMethod) && !name.endsWith('/')) unsupportedEntries += 1;

        const priority = metadataPriority(name);
        if (priority !== null && entry.uncompressedSize <= MAX_METADATA_FILE && (entry.generalPurposeBitFlag & 0x1) === 0) {
          metadataEntries.push({ entry, name, priority });
        }
        const previewPriority = imagePriority(name);
        if (previewPriority !== null) previewEntries.push({ name, priority: previewPriority, size: entry.uncompressedSize });
        zip.readEntry();
      });
      zip.once('end', () => {
        if (settled) return;
        settled = true;
        resolve();
      });
      zip.readEntry();
    });

    const metadataDocuments = [];
    let metadataBytes = 0;
    for (const candidate of metadataEntries.sort((a, b) => a.priority - b.priority || a.entry.uncompressedSize - b.entry.uncompressedSize).slice(0, 12)) {
      if (metadataBytes + candidate.entry.uncompressedSize > MAX_METADATA_TOTAL) continue;
      try {
        const buffer = await readEntry(zip, candidate.entry, MAX_METADATA_FILE);
        metadataBytes += buffer.length;
        const text = buffer.toString('utf8').replace(/^\uFEFF/, '').replace(/\0+$/g, '');
        const data = JSON5.parse(text);
        metadataDocuments.push({ path: candidate.name, data });
      } catch (error) {
        issues.push({ code: 'metadata-unreadable', severity: 'info', message: `${candidate.name}: ${error.message}` });
      }
    }

    const { paths: logicalPaths, wrapper } = unwrapLogicalPaths(names);
    const content = inferContent(logicalPaths, path, options.databaseType);
    const lowerLogical = logicalPaths.map((name) => name.toLowerCase());
    const hasKnownRoot = lowerLogical.some((name) => KNOWN_ROOTS.has(name.split('/')[0]) || name === 'info.json');

    if (entryCount === 0) issues.push({ code: 'empty-archive', severity: 'error', message: 'The archive contains no entries.' });
    if (encryptedEntries > 0) issues.push({ code: 'encrypted-content', severity: 'error', message: `${encryptedEntries} encrypted entries cannot be loaded by BeamNG.` });
    if (unsupportedEntries > 0) issues.push({ code: 'unsupported-compression', severity: 'warning', message: `${unsupportedEntries} entries use an uncommon compression method.` });
    if (wrapper) issues.push({ code: 'nested-root', severity: 'warning', message: `BeamNG content is nested inside “${wrapper}”; this archive may need repacking.` });
    if (!hasKnownRoot && content.nestedArchiveCount > 0) issues.push({ code: 'archive-bundle', severity: 'warning', message: 'This is a bundle containing other archives and may need to be unpacked first.' });
    if (!hasKnownRoot && content.nestedArchiveCount === 0) issues.push({ code: 'unrecognized-content', severity: 'warning', message: 'No standard BeamNG content root was found.' });

    const previewPath = previewEntries.sort((a, b) => a.priority - b.priority || a.size - b.size)[0]?.name ?? null;
    return {
      validArchive: true,
      fingerprint: hash.digest('hex'),
      entryCount,
      compressedBytes,
      uncompressedBytes,
      wrapper,
      ...content,
      metadata: normalizedMetadata(metadataDocuments),
      metadataDocuments,
      previewPath,
      issues,
    };
  } catch (error) {
    return {
      validArchive: false,
      fingerprint: null,
      entryCount,
      compressedBytes,
      uncompressedBytes,
      category: 'Invalid archive',
      contentTags: [],
      namespaces: { levels: [], vehicles: [], ui: [] },
      metadata: {},
      metadataDocuments: [],
      previewPath: null,
      issues: [{ code: 'invalid-archive', severity: 'error', message: error.message }],
    };
  } finally {
    try { zip.close(); } catch {}
  }
}
