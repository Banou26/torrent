// Targeted verification of TCP / uTP peer data exchange and tracker
// transports. Each scenario is a fresh Node process to avoid the
// startRuntime singleton (we can only init the Go WASM once per page).
//
// Run: node test/verify.mjs <scenario>
//
//   tcp-only   - disable uTP, prove peers connect + bytes flow over TCP
//   utp-only   - disable TCP, prove peers connect + bytes flow over uTP
//   http-trk   - use a torrent with an HTTP tracker, prove the announce
//                roundtrips through the bridge (the Debian torrent ships
//                http://bttracker.debian.org:6969/announce).

import * as net from 'node:net';
import * as dgram from 'node:dgram';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';

import { createClient, memoryStorage } from '../dist/index.js';

const here = dirname(fileURLToPath(import.meta.url));
const distDir = resolve(here, '..', 'dist');

const origFetch = globalThis.fetch;
globalThis.fetch = async (url) => {
  const u = typeof url === 'string' ? new URL(url) : url;
  if (u.protocol === 'file:') {
    const buf = await readFile(fileURLToPath(u));
    return new Response(buf, {
      status: 200,
      headers: {
        'content-type': u.pathname.endsWith('.wasm') ? 'application/wasm' : 'text/javascript',
      },
    });
  }
  return origFetch(url);
};

// Sintel - well-seeded, ~130 MB, used by webtorrent demos.
const sintel =
  'magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10' +
  '&dn=Sintel' +
  '&tr=udp%3A%2F%2Fexplodie.org%3A6969' +
  '&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969' +
  '&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337' +
  '&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337' +
  '&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969';

// Debian 13.4 netinst - its only tracker is the HTTP one at
// bttracker.debian.org, so a successful announce proves the HTTP
// tracker path through the bridge works.
const debianMagnet =
  'magnet:?xt=urn:btih:3b1de9cb7011350fa152ec47419620aa153e19e7' +
  '&dn=debian-13.4.0-amd64-netinst.iso' +
  '&tr=http%3A%2F%2Fbttracker.debian.org%3A6969%2Fannounce';

async function runScenario(scenario) {
  console.log(`=== scenario: ${scenario} ===`);

  /** @type {import('../dist/index.js').ClientOptions} */
  const opts = {};
  let magnet = sintel;
  let downloadBytes = 256 * 1024; // 256 KiB sample

  switch (scenario) {
    case 'tcp-only':
      opts.disableUTP = true;
      break;
    case 'utp-only':
      opts.disableTCP = true;
      break;
    case 'http-trk':
      magnet = debianMagnet;
      opts.disableDHT = true;
      opts.disablePEX = true;
      // Don't try to download - just verify the HTTP announce roundtrip.
      downloadBytes = 0;
      break;
    default:
      throw new Error('unknown scenario ' + scenario);
  }

  const client = await createClient({
    wasmUrl: new URL(`file://${distDir}/torrent.wasm`),
    wasmExecUrl: new URL(`file://${distDir}/wasm_exec.js`),
    net,
    dgram,
    storage: memoryStorage(),
    debug: process.env.DEBUG === '1',
    ...opts,
  });
  console.log('client created');

  const t = await client.addMagnet(magnet);
  console.log('magnet added:', t.infoHash);

  // Wait for info (long timeout - TCP-only on some networks is slow).
  const info = await Promise.race([
    t.gotInfo(),
    new Promise((_, reject) =>
      setTimeout(() => reject(new Error('gotInfo timeout (120s)')), 120_000),
    ),
  ]);
  console.log('got info:', { name: info.name, length: info.length, numPieces: info.numPieces });

  if (downloadBytes === 0) {
    // For the HTTP tracker scenario, we just want to confirm peers
    // came back from the announce (not that we downloaded anything).
    await new Promise((r) => setTimeout(r, 15_000));
    const stats = await t.stats();
    console.log('stats after announce:', stats);
    await client.close();
    return stats.totalPeers > 0;
  }

  // Get the largest file and read a chunk from offset 0.
  const files = await t.files();
  files.sort((a, b) => b.length - a.length);
  const f = files[0];
  console.log('reading first', downloadBytes, 'bytes of', f.path, '(', f.length, 'bytes total)');

  const start = Date.now();
  const buf = await Promise.race([
    f.read(0, downloadBytes),
    new Promise((_, reject) =>
      setTimeout(() => reject(new Error('file.read timeout (120s)')), 120_000),
    ),
  ]);
  const elapsedSec = (Date.now() - start) / 1000;

  if (!(buf instanceof Uint8Array)) throw new Error('expected Uint8Array');
  if (buf.length === 0) throw new Error('got zero bytes');

  const sha1 = createHash('sha1').update(buf).digest('hex');
  console.log(
    `OK: read ${buf.length} bytes in ${elapsedSec.toFixed(1)}s ` +
      `(${((buf.length / elapsedSec / 1024 / 1024) || 0).toFixed(2)} MiB/s), sha1=${sha1}`,
  );

  const stats = await t.stats();
  console.log('stats:', stats);

  await client.close();
  return true;
}

const scenario = process.argv[2];
if (!scenario) {
  console.error('usage: node test/verify.mjs <tcp-only|utp-only|http-trk>');
  process.exit(2);
}

runScenario(scenario)
  .then((ok) => {
    console.log(ok ? 'PASS' : 'FAIL');
    process.exit(ok ? 0 : 1);
  })
  .catch((err) => {
    console.error('FAILED:', err);
    process.exit(1);
  });
