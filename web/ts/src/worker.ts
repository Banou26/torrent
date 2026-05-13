// Worker entry point for @anacrolix/torrent.
//
// Pair with createClient({ worker: new Worker(...) }) on the main thread.
// Main ships its @fkn/lib `apiPromise` (resolving to the iframe-backed
// Resolvers) into us via osra; we use it to construct net/dgram Socket
// objects locally — those run in this worker, but every actual VPN
// call routes through the apiPromise (and therefore back to the main
// window's iframe + WebTransport to webvpn).
//
// Why ship the apiPromise instead of net/dgram objects? @fkn/lib's
// Server/Socket classes have EventEmitter prototypes that don't survive
// osra serialization. The apiPromise resolves to a plain Resolvers
// object whose methods osra knows how to proxy, so it crosses fine.

// Probe message BEFORE imports, so we know whether the script started
// at all even if imports fail.
try {
  (self as DedicatedWorkerGlobalScope).postMessage({
    __torrentWorkerLog: true,
    args: ['[worker] script started, about to import'],
  });
} catch {}

// Hook console.log so anything from inside @fkn/lib (or other deps) gets
// forwarded to the main page along with our own logs.
const origConsoleLog = console.log.bind(console);
console.log = (...args: unknown[]) => {
  origConsoleLog(...args);
  try {
    (self as DedicatedWorkerGlobalScope).postMessage({
      __torrentWorkerLog: true,
      args: args.map((a) => (a instanceof Error ? `${a.name}: ${a.message}` : a)),
    });
  } catch {}
};

import { expose } from 'osra';
// @fkn/lib's dom.ts was patched to be safe to import without a window
// (returns null instead of throwing); the apiPromise rejects in that
// case, but we override it via constructor options below so it never
// matters.
import { net as fknNet, dgram as fknDgram } from '@fkn/lib';

import { createBridge } from './bridge.js';
import { loadWasm, type LoadOptions } from './wasm.js';
import type {
  NodeDgramModule,
  NodeDgramSocket,
  NodeNetModule,
  NodeServer,
  NodeSocket,
  StorageAdapter,
} from './types.js';

// Resolvers is whatever @fkn/lib exposes — osra-proxied into the worker.
type Resolvers = Record<string, (...args: unknown[]) => unknown>;

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
  /** osra-proxied apiPromise; resolves to main's @fkn/lib Resolvers. */
  apiPromise: Promise<Resolvers>;
  hasNet: boolean;
  hasDgram: boolean;
}

export const OSRA_KEY = 'anacrolix-torrent-worker';

// Forward worker-side console.log to main so it lands in the page
// console (DevTools-per-worker is awkward to open through the
// automation plugin).
const log = (...args: unknown[]): void => {
  // eslint-disable-next-line no-console
  console.log(...args);
  try {
    (self as DedicatedWorkerGlobalScope).postMessage({
      __torrentWorkerLog: true,
      args: args.map((a) => (a instanceof Error ? `${a.name}: ${a.message}` : a)),
    });
  } catch {
    // Non-cloneable payload — drop.
  }
};

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

// Build a NodeNetModule that constructs @fkn/lib Sockets/Servers
// parameterised on the injected apiPromise. The bridge.ts code calls
// `net.createConnection` / `net.createServer` — these factories pin
// each instance to main's apiPromise so any internal `api['webVpn...']`
// call routes back through osra → main → iframe → webvpn.
function makeNetForApiPromise(apiPromise: Promise<Resolvers>): NodeNetModule {
  return {
    createConnection(opts) {
      // @fkn/lib's Socket exposes the EventEmitter surface our bridge.ts
      // expects, plus the `connect()` we trigger below to start the dial.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const s = new (fknNet as any).Socket({ apiPromise }) as NodeSocket & {
        connect: (opts: { host: string; port: number }) => unknown;
      };
      s.connect({ host: opts.host, port: opts.port });
      return s;
    },
    createServer() {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return new (fknNet as any).Server({ apiPromise }) as NodeServer;
    },
  };
}

function makeDgramForApiPromise(apiPromise: Promise<Resolvers>): NodeDgramModule {
  return {
    createSocket(type) {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return new (fknDgram as any).Socket({ type, apiPromise }) as NodeDgramSocket;
    },
  };
}

const boot = async (): Promise<void> => {
  log('[worker] awaiting osra handshake');
  const host = (await expose(workerExports, {
    transport: self,
    key: OSRA_KEY,
  })) as unknown as HostExports;
  log('[worker] osra handshake done; loading wasm');

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

  const bridge = createBridge({
    ...(host.hasNet ? { net: makeNetForApiPromise(host.apiPromise) } : {}),
    ...(host.hasDgram ? { dgram: makeDgramForApiPromise(host.apiPromise) } : {}),
    storage: host.storage,
    onReady: () => signalReady(),
  });
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
