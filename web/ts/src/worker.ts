// Worker entry point for @anacrolix/torrent.
//
// Pair with createClient({ worker: new Worker(...) }) on the main thread.
// Main calls @fkn/lib's `exposeApi({ transport: worker })` to re-expose
// its iframe-backed Resolvers over this worker; we call `connectApi`
// here and feed the result to `createFkn` to get net/dgram modules. The
// Go scheduler then runs entirely inside this worker; only the actual
// webvpn calls hop back to main and out to the iframe.

import { expose } from 'osra';
import { connectApi, createFkn } from '@fkn/lib';

import { createBridge } from './bridge.js';
import { loadWasm, type LoadOptions } from './wasm.js';
import type { StorageAdapter } from './types.js';

interface WasmTorrentApi {
  newClient(opts: object): Promise<{ id: number }>;
  closeClient(id: number): Promise<void>;
  addMagnet(clientId: number, uri: string): Promise<{ id: number; infoHash: string }>;
  addTorrent(clientId: number, bytes: Uint8Array): Promise<{ id: number; infoHash: string }>;
  torrentGotInfo(id: number): Promise<void>;
  torrentInfo(id: number): Promise<unknown>;
  torrentFiles(id: number): Promise<unknown[]>;
  torrentStats(id: number): Promise<unknown>;
  torrentDownloadAll(id: number): Promise<void>;
  torrentDrop(id: number): Promise<void>;
  fileRead(id: number, offset: number, length: number): Promise<Uint8Array>;
}

interface HostExports {
  wasmUrl: string;
  wasmExecUrl?: string;
  argv?: string[];
  env?: Record<string, string>;
  storage: StorageAdapter;
  hasNet: boolean;
  hasDgram: boolean;
}

export const OSRA_KEY = 'anacrolix-torrent-worker';

// Forward worker-side console.log to main so it lands in the page
// console (DevTools-per-worker is awkward to open through the
// automation plugin). Hook console.log eagerly so anything from inside
// @fkn/lib and its deps also surfaces.
const log = (...args: unknown[]): void => {
  // eslint-disable-next-line no-console
  origConsoleLog(...args);
  try {
    (self as DedicatedWorkerGlobalScope).postMessage({
      __torrentWorkerLog: true,
      args: args.map((a) => (a instanceof Error ? `${a.name}: ${a.message}` : a)),
    });
  } catch {
    // Non-cloneable payload — drop.
  }
};
const origConsoleLog = console.log.bind(console);
console.log = (...args: unknown[]) => log(...args);

self.addEventListener('error', (e) => {
  log('[worker] error', e.message, e.filename, e.lineno, e.colno);
});
self.addEventListener('unhandledrejection', (e) => {
  log('[worker] unhandledrejection', (e.reason as Error)?.message ?? e.reason);
});
log('[worker] booting');

let resolveWasm!: (api: WasmTorrentApi) => void;
let rejectWasm!: (err: Error) => void;
const wasmApiPromise = new Promise<WasmTorrentApi>((resolve, reject) => {
  resolveWasm = resolve;
  rejectWasm = reject;
});

const workerExports = {
  async newClient(opts: object) {
    return (await wasmApiPromise).newClient(opts);
  },
  async closeClient(id: number) {
    return (await wasmApiPromise).closeClient(id);
  },
  async addMagnet(clientId: number, uri: string) {
    return (await wasmApiPromise).addMagnet(clientId, uri);
  },
  async addTorrent(clientId: number, bytes: Uint8Array) {
    return (await wasmApiPromise).addTorrent(clientId, bytes);
  },
  async torrentGotInfo(id: number) {
    return (await wasmApiPromise).torrentGotInfo(id);
  },
  async torrentInfo(id: number) {
    return (await wasmApiPromise).torrentInfo(id);
  },
  async torrentFiles(id: number) {
    return (await wasmApiPromise).torrentFiles(id);
  },
  async torrentStats(id: number) {
    return (await wasmApiPromise).torrentStats(id);
  },
  async torrentDownloadAll(id: number) {
    return (await wasmApiPromise).torrentDownloadAll(id);
  },
  async torrentDrop(id: number) {
    return (await wasmApiPromise).torrentDrop(id);
  },
  async fileRead(id: number, offset: number, length: number) {
    return (await wasmApiPromise).fileRead(id, offset, length);
  },
};

const boot = async (): Promise<void> => {
  log('[worker] awaiting osra handshake');
  // Start the @fkn/lib bridge handshake concurrently with our own torrent
  // RPC handshake. Both osra channels share the worker's MessagePort but
  // use different keys (fkn's BRIDGE_KEY = 'fkn-api-bridge', ours OSRA_KEY)
  // so they don't conflict.
  const fknApiPromise = connectApi({ transport: self as unknown as Worker });
  const host = (await expose(workerExports, {
    transport: self,
    key: OSRA_KEY,
  })) as unknown as HostExports;
  log('[worker] osra handshake done; loading wasm');

  const fkn = createFkn({ api: fknApiPromise });

  const loadOpts: LoadOptions = {
    wasmUrl: host.wasmUrl,
    ...(host.wasmExecUrl !== undefined ? { wasmExecUrl: host.wasmExecUrl } : {}),
    ...(host.argv !== undefined ? { argv: host.argv } : {}),
    ...(host.env !== undefined ? { env: host.env } : {}),
  };

  let signalReady!: () => void;
  const readyPromise = new Promise<void>((r) => {
    signalReady = r;
  });

  type BridgeOpts = Parameters<typeof createBridge>[0];
  const bridgeOpts: BridgeOpts = {
    storage: host.storage,
    onReady: () => signalReady(),
  };
  // @fkn/lib's net/dgram are Node-compatible at runtime; the structural
  // type just doesn't line up with our minimal Node interface, so cast.
  if (host.hasNet) bridgeOpts.net = fkn.net as unknown as NonNullable<BridgeOpts['net']>;
  if (host.hasDgram) bridgeOpts.dgram = fkn.dgram as unknown as NonNullable<BridgeOpts['dgram']>;
  const bridge = createBridge(bridgeOpts);
  (globalThis as { __torrentBridge?: object }).__torrentBridge = bridge;

  await loadWasm(loadOpts, readyPromise);
  log('[worker] wasm ready');
  const api = (globalThis as { __torrent?: WasmTorrentApi }).__torrent;
  if (!api) throw new Error('torrent wasm did not install globalThis.__torrent');
  resolveWasm(api);
};

boot().catch((err) => {
  rejectWasm(err instanceof Error ? err : new Error(String(err)));
  log('[worker] boot failed', (err as Error).message);
});
