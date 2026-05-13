// Public TypeScript API for @anacrolix/torrent.
//
// Usage in a bundler-driven browser app:
//
//   import { createClient } from '@anacrolix/torrent';
//   import { opfsStorage } from '@anacrolix/torrent/storage/opfs';
//   import * as fkn from '@fkn/lib';
//
//   const client = await createClient({
//     wasmUrl: new URL('@anacrolix/torrent/wasm', import.meta.url),
//     net: fkn.net,
//     dgram: fkn.dgram,
//     storage: await opfsStorage(),
//   });
//   const t = await client.addMagnet('magnet:?xt=urn:btih:...');
//   const info = await t.gotInfo();
//   const [file] = await t.files();
//   const bytes = await file.read(0, 4096);

import { createBridge } from './bridge.js';
import { loadWasm, type LoadOptions } from './wasm.js';
import { memoryStorage } from './storage/memory.js';
import type {
  ClientOptions,
  FileHandle,
  NodeDgramModule,
  NodeNetModule,
  StorageAdapter,
  TorrentInfo,
  TorrentStats,
} from './types.js';

export type {
  ClientOptions,
  FileHandle,
  NodeDgramModule,
  NodeDgramSocket,
  NodeNetModule,
  NodeServer,
  NodeSocket,
  StorageAdapter,
  StorageTorrent,
  StorageTorrentOpenInfo,
  TorrentHandle,
  TorrentInfo,
  TorrentStats,
} from './types.js';

// Shape Go installs at globalThis.__torrent. Each method returns a Promise.
interface WasmTorrentApi {
  newClient(opts: object): Promise<{ id: number }>;
  closeClient(id: number): Promise<void>;
  addMagnet(clientId: number, uri: string): Promise<{ id: number; infoHash: string }>;
  addTorrent(clientId: number, bytes: Uint8Array): Promise<{ id: number; infoHash: string }>;
  torrentGotInfo(id: number): Promise<void>;
  torrentInfo(id: number): Promise<TorrentInfo>;
  torrentFiles(id: number): Promise<FileHandle[]>;
  torrentStats(id: number): Promise<TorrentStats>;
  torrentDownloadAll(id: number): Promise<void>;
  torrentDrop(id: number): Promise<void>;
  fileRead(id: number, offset: number, length: number): Promise<Uint8Array>;
}

declare global {
  // eslint-disable-next-line no-var
  var __torrent: WasmTorrentApi | undefined;
  // eslint-disable-next-line no-var
  var __torrentBridge: object | undefined;
}

let wasmReady: Promise<WasmTorrentApi> | null = null;

function startRuntime(
  load: LoadOptions,
  net: NodeNetModule | undefined,
  dgram: NodeDgramModule | undefined,
  storage: StorageAdapter,
): Promise<WasmTorrentApi> {
  if (wasmReady) return wasmReady;
  let signalReady!: () => void;
  const readyPromise = new Promise<void>((r) => (signalReady = r));
  const bridge = createBridge({
    ...(net !== undefined ? { net } : {}),
    ...(dgram !== undefined ? { dgram } : {}),
    storage,
    onReady: () => signalReady(),
  });
  (globalThis as { __torrentBridge?: object }).__torrentBridge = bridge;
  wasmReady = loadWasm(load, readyPromise).then(() => {
    const api = (globalThis as { __torrent?: WasmTorrentApi }).__torrent;
    if (!api) throw new Error('torrent wasm did not install globalThis.__torrent');
    return api;
  });
  return wasmReady;
}

export interface CreateClientOptions extends ClientOptions, LoadOptions {}

export async function createClient(opts: CreateClientOptions): Promise<Client> {
  const storage = opts.storage ?? memoryStorage();
  if (!opts.net && !opts.dgram) {
    // eslint-disable-next-line no-console
    console.warn(
      '@anacrolix/torrent: no `net` or `dgram` module supplied. Pass @fkn/lib (or a custom Node-compatible module) for any peer connectivity.',
    );
  }

  const api = await startRuntime(opts, opts.net, opts.dgram, storage);

  const clientOpts: Record<string, unknown> = {};
  for (const key of [
    'disableTrackers',
    'disablePEX',
    'disableDHT',
    'disableTCP',
    'disableUTP',
    'seed',
    'listenPort',
    'peerID',
    'debug',
  ] as const) {
    if (opts[key] !== undefined) clientOpts[key] = opts[key];
  }
  if (!opts.net) clientOpts.disableTCP = true;
  if (!opts.dgram) {
    clientOpts.disableUTP = true;
    clientOpts.disableDHT = true;
  }

  const { id } = await api.newClient(clientOpts);
  return new Client(id, api);
}

export class Client {
  /** @internal */
  constructor(readonly id: number, private readonly api: WasmTorrentApi) {}

  async addMagnet(uri: string): Promise<Torrent> {
    const h = await this.api.addMagnet(this.id, uri);
    return new Torrent(h.id, h.infoHash, this.api);
  }

  async addTorrent(metainfo: Uint8Array | ArrayBuffer): Promise<Torrent> {
    const bytes = metainfo instanceof Uint8Array ? metainfo : new Uint8Array(metainfo);
    const h = await this.api.addTorrent(this.id, bytes);
    return new Torrent(h.id, h.infoHash, this.api);
  }

  async close(): Promise<void> {
    await this.api.closeClient(this.id);
  }
}

export class Torrent {
  /** @internal */
  constructor(
    readonly id: number,
    readonly infoHash: string,
    private readonly api: WasmTorrentApi,
  ) {}

  /** Resolves once the info dictionary has been fetched (for magnet links). */
  async gotInfo(): Promise<TorrentInfo> {
    await this.api.torrentGotInfo(this.id);
    return await this.api.torrentInfo(this.id);
  }

  async info(): Promise<TorrentInfo> {
    return await this.api.torrentInfo(this.id);
  }

  async files(): Promise<File[]> {
    const fs = await this.api.torrentFiles(this.id);
    return fs.map((f) => new File(f, this.api));
  }

  async stats(): Promise<TorrentStats> {
    return await this.api.torrentStats(this.id);
  }

  async downloadAll(): Promise<void> {
    await this.api.torrentDownloadAll(this.id);
  }

  async drop(): Promise<void> {
    await this.api.torrentDrop(this.id);
  }
}

export class File {
  /** @internal */
  constructor(private readonly handle: FileHandle, private readonly api: WasmTorrentApi) {}

  get path(): string {
    return this.handle.path;
  }
  get length(): number {
    return this.handle.length;
  }
  get offset(): number {
    return this.handle.offset;
  }

  async read(offset: number, length: number): Promise<Uint8Array> {
    return await this.api.fileRead(this.handle.id, offset, length);
  }

  /**
   * Return a ReadableStream of the file's bytes. Useful for piping to
   * <video src=blob:...> or fetch responses.
   */
  stream(opts: { chunkSize?: number } = {}): ReadableStream<Uint8Array> {
    const chunkSize = opts.chunkSize ?? 64 * 1024;
    const total = this.handle.length;
    const api = this.api;
    const id = this.handle.id;
    let offset = 0;
    return new ReadableStream<Uint8Array>({
      async pull(controller) {
        if (offset >= total) {
          controller.close();
          return;
        }
        const remaining = total - offset;
        const len = remaining < chunkSize ? Number(remaining) : chunkSize;
        const chunk = await api.fileRead(id, offset, len);
        if (chunk.length === 0) {
          controller.close();
          return;
        }
        offset += chunk.length;
        controller.enqueue(chunk);
      },
    });
  }
}

export { memoryStorage } from './storage/memory.js';
export { opfsStorage } from './storage/opfs.js';
