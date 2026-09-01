import { resolve } from 'node:path';
import { loadConfig } from './src/config.js';
import { loadCatalog } from './src/catalog.js';
import { preserveAll } from './src/operations.js';
import { catalogStats, scanLibrary } from './src/scanner.js';
import { diagnoseRuntime } from './src/runtime-diagnostics.js';

const config = await loadConfig();
const command = process.argv[2] ?? 'stats';

function formatBytes(bytes) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

function printStats(catalog) {
  const stats = catalogStats(catalog);
  console.log(JSON.stringify({ ...stats, size: formatBytes(stats.bytes) }, null, 2));
}

async function scan() {
  let lastPrinted = -1;
  const catalog = await scanLibrary(config, {
    onProgress(progress) {
      if (progress.phase !== 'inspect') return;
      const percent = progress.total === 0 ? 100 : Math.floor(progress.completed * 100 / progress.total);
      if (percent >= lastPrinted + 5 || progress.completed === progress.total) {
        process.stdout.write(`\rInspecting archives: ${progress.completed}/${progress.total} (${percent}%)`);
        lastPrinted = percent;
      }
    },
  });
  process.stdout.write('\n');
  return catalog;
}

if (command === 'scan') {
  printStats(await scan());
} else if (command === 'preserve') {
  const catalog = await scan();
  const before = catalogStats(catalog);
  console.log(`Moving ${before.enabled + before.loose} archives into the managed library...`);
  const result = await preserveAll(config);
  console.log(`Moved ${result.operations.length} archives; ${result.errors.length} failed.`);
  if (result.errors.length > 0) {
    for (const error of result.errors) console.error(`${error.path}: ${error.message}`);
    process.exitCode = 1;
  }
  printStats(result.catalog);
} else if (command === 'diagnose') {
  const directories = process.argv.length > 3 ? process.argv.slice(3) : [resolve(config.beamngRoot, 'current')];
  const result = await diagnoseRuntime(config, directories);
  console.log(JSON.stringify(result.summary, null, 2));
  printStats(result.catalog);
} else if (command === 'stats') {
  printStats(await loadCatalog(config));
} else {
  console.error('Usage: node cli.js [scan|preserve|diagnose [log-directory...]|stats]');
  process.exitCode = 2;
}
