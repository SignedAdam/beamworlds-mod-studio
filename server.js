import { createServer } from 'node:http';
import { randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { extname, resolve } from 'node:path';
import { spawn } from 'node:child_process';
import { APP_DIR, loadConfig } from './src/config.js';
import { loadCatalog } from './src/catalog.js';
import { catalogStats, scanLibrary } from './src/scanner.js';
import { preserveAll, setEnabled, setManyEnabled, updateMod } from './src/operations.js';
import { diagnoseRuntime } from './src/runtime-diagnostics.js';

const config = await loadConfig();
const token = randomBytes(32).toString('hex');
const allowedHosts = new Set([`127.0.0.1:${config.port}`, `localhost:${config.port}`]);
const allowedOrigins = new Set([`http://127.0.0.1:${config.port}`, `http://localhost:${config.port}`]);
const publicDir = resolve(APP_DIR, 'public');
const scanState = {
  running: false,
  phase: 'idle',
  completed: 0,
  total: 0,
  current: null,
  error: null,
};

const MIME = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
};

function sendJson(response, status, value) {
  response.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff',
  });
  response.end(JSON.stringify(value));
}

function sendError(response, status, error) {
  sendJson(response, status, { error: error instanceof Error ? error.message : String(error) });
}

async function bodyJson(request) {
  const chunks = [];
  let bytes = 0;
  for await (const chunk of request) {
    bytes += chunk.length;
    if (bytes > 1024 * 1024) throw new Error('Request body is too large');
    chunks.push(chunk);
  }
  if (bytes === 0) return {};
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function requireMutationAccess(request) {
  if (request.headers['x-mod-manager-token'] !== token) throw new Error('Invalid manager security token');
  const origin = request.headers.origin;
  if (origin && !allowedOrigins.has(origin)) throw new Error('Invalid request origin');
}

async function startScan() {
  if (scanState.running) return;
  Object.assign(scanState, { running: true, phase: 'discover', completed: 0, total: 0, current: null, error: null });
  try {
    await scanLibrary(config, {
      onProgress(progress) {
        Object.assign(scanState, progress);
      },
    });
  } catch (error) {
    scanState.error = error.message;
    scanState.phase = 'error';
  } finally {
    scanState.running = false;
  }
}

async function apiRoute(request, response, url) {
  if (request.method === 'GET' && url.pathname === '/api/state') {
    const catalog = await loadCatalog(config);
    return sendJson(response, 200, {
      token,
      scan: scanState,
      stats: catalogStats(catalog),
      lastScan: catalog.lastScan,
      lastDiagnostics: catalog.lastDiagnostics ?? null,
      mods: catalog.mods,
      paths: {
        activeModsDir: config.activeModsDir,
        libraryDir: config.libraryDir,
        gameInstallDir: config.gameInstallDir,
      },
    });
  }

  if (request.method !== 'GET') requireMutationAccess(request);

  if (request.method === 'POST' && url.pathname === '/api/scan') {
    if (!scanState.running) void startScan();
    return sendJson(response, 202, { scan: scanState });
  }
  if (request.method === 'POST' && url.pathname === '/api/diagnose') {
    const result = await diagnoseRuntime(config);
    return sendJson(response, 200, {
      diagnostics: result.summary,
      stats: catalogStats(result.catalog),
    });
  }

  if (request.method === 'POST' && url.pathname === '/api/preserve') {
    const result = await preserveAll(config);
    return sendJson(response, result.errors.length > 0 ? 207 : 200, {
      moved: result.operations.length,
      errors: result.errors,
      stats: catalogStats(result.catalog),
    });
  }

  if (request.method === 'POST' && url.pathname === '/api/mods/bulk') {
    const input = await bodyJson(request);
    if (typeof input.enabled !== 'boolean') throw new Error('Enabled must be true or false');
    const result = await setManyEnabled(config, input.ids, input.enabled);
    return sendJson(response, result.errors.length > 0 ? 207 : 200, {
      moved: result.operations.length,
      errors: result.errors,
      stats: catalogStats(result.catalog),
    });
  }

  const actionMatch = url.pathname.match(/^\/api\/mods\/([0-9a-f-]+)\/(enable|disable)$/i);
  if (request.method === 'POST' && actionMatch) {
    const result = await setEnabled(config, actionMatch[1], actionMatch[2] === 'enable');
    return sendJson(response, 200, {
      operation: result.operation,
      stats: catalogStats(result.catalog),
    });
  }

  const modMatch = url.pathname.match(/^\/api\/mods\/([0-9a-f-]+)$/i);
  if (request.method === 'PATCH' && modMatch) {
    const result = await updateMod(config, modMatch[1], await bodyJson(request));
    return sendJson(response, 200, { mod: result.mod, stats: catalogStats(result.catalog) });
  }

  if (request.method === 'POST' && url.pathname === '/api/reveal') {
    const input = await bodyJson(request);
    const catalog = await loadCatalog(config);
    const mod = catalog.mods.find((candidate) => candidate.id === input.id);
    if (!mod || mod.missing) throw new Error('Mod file is unavailable');
    const child = spawn('explorer.exe', ['/select,', mod.path], { detached: true, stdio: 'ignore' });
    child.unref();
    return sendJson(response, 200, { ok: true });
  }

  return sendError(response, 404, 'API route not found');
}

async function staticRoute(response, url) {
  const requestPath = url.pathname === '/' ? 'index.html' : decodeURIComponent(url.pathname.slice(1));
  if (!/^[a-zA-Z0-9._/-]+$/.test(requestPath) || requestPath.includes('..')) return sendError(response, 404, 'Not found');
  const file = resolve(publicDir, requestPath);
  if (!file.startsWith(publicDir)) return sendError(response, 404, 'Not found');
  try {
    const data = await readFile(file);
    response.writeHead(200, {
      'Content-Type': MIME[extname(file).toLowerCase()] ?? 'application/octet-stream',
      'Cache-Control': 'no-cache',
      'Content-Security-Policy': "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
      'Referrer-Policy': 'no-referrer',
      'X-Content-Type-Options': 'nosniff',
      'X-Frame-Options': 'DENY',
    });
    response.end(data);
  } catch (error) {
    if (error.code === 'ENOENT') return sendError(response, 404, 'Not found');
    throw error;
  }
}

const server = createServer(async (request, response) => {
  if (!allowedHosts.has(String(request.headers.host).toLowerCase())) {
    return sendError(response, 403, 'Invalid request host');
  }
  try {
    const url = new URL(request.url, `http://127.0.0.1:${config.port}`);
    if (url.pathname.startsWith('/api/')) await apiRoute(request, response, url);
    else await staticRoute(response, url);
  } catch (error) {
    sendError(response, error.message?.includes('security token') || error.message?.includes('origin') ? 403 : 400, error);
  }
});

server.on('error', async (error) => {
  if (error.code === 'EADDRINUSE') {
    const address = `http://127.0.0.1:${config.port}`;
    try {
      const response = await fetch(`${address}/api/state`);
      const state = await response.json();
      if (!Array.isArray(state.mods)) throw new Error('The service on this port is not BeamNG Mod Manager');
      console.log(`BeamNG Mod Manager is already running at ${address}`);
      if (!process.argv.includes('--no-open')) {
        const child = spawn('explorer.exe', [address], { detached: true, stdio: 'ignore' });
        child.unref();
      }
      process.exit(0);
    } catch (probeError) {
      console.error(`Port ${config.port} is already in use: ${probeError.message}`);
      process.exit(1);
    }
  }
  console.error(error);
  process.exit(1);
});

server.listen(config.port, '127.0.0.1', async () => {
  const address = `http://127.0.0.1:${config.port}`;
  console.log(`BeamNG Mod Manager ready at ${address}`);
  const catalog = await loadCatalog(config);
  if (!catalog.lastScan) void startScan();
  if (!process.argv.includes('--no-open')) {
    const child = spawn('explorer.exe', [address], { detached: true, stdio: 'ignore' });
    child.unref();
  }
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
