// Node-based smoke test for the WASM module.
//
// Uses Node's built-in `net` and `dgram` (Node API == the API @fkn/lib
// exposes), wires them through the bridge, and asks the Go client to
// fetch info for a well-seeded magnet over real DHT/trackers.
//
// Run: cd web/ts && node test/smoke.mjs

import * as net from 'node:net';
import * as dgram from 'node:dgram';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { readFile } from 'node:fs/promises';

import { createClient, memoryStorage } from '../dist/index.js';

const here = dirname(fileURLToPath(import.meta.url));
const distDir = resolve(here, '..', 'dist');

// Patch global fetch so the wasm loader can read the local files.
// (loadWasm uses fetch(url) and instantiateStreaming.)
const origFetch = globalThis.fetch;
globalThis.fetch = async (url) => {
  const u = typeof url === 'string' ? new URL(url) : url;
  if (u.protocol === 'file:') {
    const buf = await readFile(fileURLToPath(u));
    return new Response(buf, {
      status: 200,
      headers: { 'content-type': u.pathname.endsWith('.wasm') ? 'application/wasm' : 'text/javascript' },
    });
  }
  return origFetch(url);
};

// Sintel — Creative-Commons movie used in webtorrent demos. Magnet from
// https://webtorrent.io/free-torrents.
const sintel =
  'magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337';

async function main() {
  console.log('starting smoke test');

  const client = await createClient({
    wasmUrl: new URL(`file://${distDir}/torrent.wasm`),
    wasmExecUrl: new URL(`file://${distDir}/wasm_exec.js`),
    net,
    dgram,
    storage: memoryStorage(),
  });
  console.log('client created');

  const t = await client.addMagnet(sintel);
  console.log('added magnet, infoHash =', t.infoHash);

  const timeout = setTimeout(() => {
    console.error('TIMEOUT: torrent.gotInfo() did not resolve within 60s');
    process.exit(1);
  }, 60_000);

  const info = await t.gotInfo();
  clearTimeout(timeout);
  console.log('got info:', info);

  const files = await t.files();
  console.log('files:');
  for (const f of files) console.log('  ', f.path, f.length);

  const stats = await t.stats();
  console.log('stats:', stats);

  await client.close();
  console.log('client closed');
}

main().catch((err) => {
  console.error('smoke test failed:', err);
  process.exit(1);
});
