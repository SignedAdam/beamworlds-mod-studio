// SPDX-License-Identifier: GPL-3.0-only
// Run after the frontend build: node build/collect-notices.mjs
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const app = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = join(app, 'dist', 'third-party');
const sections = [
  'THIRD-PARTY SOFTWARE NOTICES',
  'Original license and notice texts for bundled application dependencies.',
  'The separately bundled AI runtime has additional notices in ai-runtime/.',
  'Generated from the installed, locked dependencies by build/collect-notices.mjs.',
];

function addLicenses(label, directory) {
  const names = readdirSync(directory, { withFileTypes: true })
    .filter(entry => entry.isFile() && /^(licen[cs]e|copying|notice|patents)([._-].*)?$/i.test(entry.name))
    .map(entry => entry.name).sort();
  if (!names.some(name => /^(licen[cs]e|copying)([._-].*)?$/i.test(name))) {
    throw new Error(`Missing full license text for ${label} in ${directory}; review before release.`);
  }
  for (const name of names) {
    sections.push(`${'='.repeat(72)}\n${label} — ${name}\n${'='.repeat(72)}\n\n${readFileSync(join(directory, name), 'utf8')}`);
  }
}

const goRoot = execFileSync('go', ['env', 'GOROOT'], { cwd: app, encoding: 'utf8' }).trim();
addLicenses('Go standard library and runtime', goRoot);

// Only modules used by the compiled application; local app/modkit code uses the root GPL.
const moduleRows = execFileSync('go', ['list', '-deps', '-tags', 'production', '-f',
  '{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}', '.'],
  { cwd: app, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
const ownModules = new Set(['github.com/SignedAdam/beamng-mod-studio', 'github.com/SignedAdam/beamworlds-modkit']);
const modules = new Map();
for (const row of moduleRows.split(/\r?\n/).filter(Boolean)) {
  const [name, version, directory] = row.split('|');
  if (!ownModules.has(name)) modules.set(`${name}@${version}`, directory);
}
if (!modules.size) throw new Error('No third-party Go modules found; refusing an empty attribution list.');
for (const [label, directory] of [...modules].sort(([a], [b]) => a.localeCompare(b))) {
  addLicenses(`Go: ${label}`, directory);
}

// Lockfile paths retain nested package versions; do not guess hoisted paths or skip missing licenses.
const frontend = join(app, 'frontend');
const lock = JSON.parse(readFileSync(join(frontend, 'package-lock.json'), 'utf8'));
if (!lock.packages) throw new Error('Expected an npm lockfile with package paths.');
let npmCount = 0;
for (const [relative, entry] of Object.entries(lock.packages).sort(([a], [b]) => a.localeCompare(b))) {
  if (!relative || entry.dev || entry.link) continue;
  if (!relative.startsWith('node_modules/')) throw new Error(`Unexpected dependency path: ${relative}`);
  const directory = resolve(frontend, relative);
  if (!directory.startsWith(resolve(frontend, 'node_modules') + sep)) {
    throw new Error(`Dependency path escapes node_modules: ${relative}`);
  }
  const installed = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'));
  if (installed.version !== entry.version) throw new Error(`Installed version differs from lockfile: ${relative}`);
  // Wails' npm tarball ships dist/types only; its license is in the matching Go module.
  const licenseDirectory = installed.name === '@wailsio/runtime'
    && installed.repository?.url === 'git+https://github.com/wailsapp/wails.git'
    ? modules.get(`github.com/wailsapp/wails/v3@v${installed.version}`)
    : directory;
  if (!licenseDirectory) throw new Error(`No matching upstream license source for ${installed.name}@${installed.version}`);
  addLicenses(`npm: ${installed.name}@${installed.version}`, licenseDirectory);
  npmCount++;
}
if (!npmCount) throw new Error('No production npm dependencies found.');
sections.push(`Font: Inter\n\n${readFileSync(join(frontend, 'Inter Font License.txt'), 'utf8')}`);

// Read every mandatory input before publishing the consolidated file.
const runtime = join(app, 'bin', 'runtime');
readFileSync(join(runtime, 'LICENSE'));
readFileSync(join(runtime, 'THIRD-PARTY-NOTICES.txt'));
mkdirSync(join(output, 'ai-runtime'), { recursive: true });
copyFileSync(join(runtime, 'LICENSE'), join(output, 'ai-runtime', 'LICENSE-MIT'));
copyFileSync(join(runtime, 'THIRD-PARTY-NOTICES.txt'), join(output, 'ai-runtime', 'THIRD-PARTY-NOTICES.txt'));
writeFileSync(join(output, 'NOTICES.txt'), sections.join('\n\n') + '\n', 'utf8');
console.log(`Collected ${modules.size} Go modules, ${npmCount} npm packages, Inter, and AI runtime notices.`);
