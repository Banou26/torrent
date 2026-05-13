// Public type definitions for the browser torrent client.
//
// The shapes below mirror what the Go-side WASM module exposes on
// globalThis.__torrent. Each `*Handle` is an opaque numeric handle the
// caller never inspects.

export interface ClientHandle {
  readonly id: number;
}

export interface TorrentHandle {
  readonly id: number;
  readonly infoHash: string;
}

export interface FileHandle {
  readonly id: number;
  readonly path: string;
  readonly length: number;
  readonly offset: number;
}

export interface TorrentInfo {
  readonly name: string;
  readonly infoHash: string;
  readonly length: number;
  readonly pieceLength: number;
  readonly numPieces: number;
}

export interface TorrentStats {
  readonly bytesCompleted: number;
  readonly bytesMissing: number;
  readonly activePeers: number;
  readonly connectedSeeders: number;
  readonly totalPeers: number;
  readonly halfOpenPeers: number;
}

// --------------------------------------------------------------------------
// Node-compatible net / dgram modules.
//
// @fkn/lib exports `net` and `dgram` with the same shape as Node's
// built-in modules, restricted to what this library actually uses. We
// define the narrowest interface that lets the bridge drive them
// directly — so any module with the same API (including the real Node
// modules) can be substituted.

export interface NodeSocket {
  readonly remoteAddress?: string;
  readonly remotePort?: number;
  readonly localAddress?: string;
  readonly localPort?: number;
  on(event: 'connect', cb: () => void): void;
  on(event: 'data', cb: (chunk: Uint8Array) => void): void;
  on(event: 'end', cb: () => void): void;
  on(event: 'close', cb: () => void): void;
  on(event: 'error', cb: (err: Error) => void): void;
  write(data: Uint8Array, cb?: (err?: Error) => void): boolean;
  end(): void;
  destroy(err?: Error): void;
}

export interface NodeServer {
  on(event: 'connection', cb: (sock: NodeSocket) => void): void;
  on(event: 'error', cb: (err: Error) => void): void;
  on(event: 'listening', cb: () => void): void;
  listen(port: number, host: string, cb?: () => void): void;
  address(): { address: string; port: number } | string | null;
  close(cb?: () => void): void;
}

export interface NodeNetModule {
  createConnection(opts: { host: string; port: number }): NodeSocket;
  createServer(): NodeServer;
}

export interface NodeDgramSocket {
  on(
    event: 'message',
    cb: (msg: Uint8Array, rinfo: { address: string; port: number }) => void,
  ): void;
  on(event: 'error', cb: (err: Error) => void): void;
  on(event: 'listening', cb: () => void): void;
  bind(port: number, address: string, cb?: () => void): void;
  send(
    msg: Uint8Array,
    port: number,
    address: string,
    cb?: (err?: Error | null, bytes?: number) => void,
  ): void;
  address(): { address: string; port: number };
  close(cb?: () => void): void;
}

export interface NodeDgramModule {
  createSocket(type: 'udp4' | 'udp6'): NodeDgramSocket;
}

// --------------------------------------------------------------------------
// Storage adapter

export interface StorageTorrentOpenInfo {
  readonly infoHash: string;
  readonly name: string;
  readonly pieceLength: number;
  readonly numPieces: number;
}

export interface StorageTorrent {
  pieceReadAt(pieceIndex: number, offset: number, length: number): Promise<Uint8Array>;
  pieceWriteAt(pieceIndex: number, offset: number, data: Uint8Array): Promise<number>;
  pieceMarkComplete(pieceIndex: number): Promise<void>;
  pieceMarkNotComplete(pieceIndex: number): Promise<void>;
  pieceCompletion(pieceIndex: number): Promise<{ ok: boolean; complete: boolean }>;
  close(): Promise<void>;
}

export interface StorageAdapter {
  open(info: StorageTorrentOpenInfo): Promise<StorageTorrent>;
}

// --------------------------------------------------------------------------
// Client options.

export interface ClientOptions {
  /** Disable announcing to BitTorrent trackers. */
  disableTrackers?: boolean;
  /** Disable PEX. */
  disablePEX?: boolean;
  /** Disable the Kademlia DHT. */
  disableDHT?: boolean;
  /** Disable plain TCP peer connections. */
  disableTCP?: boolean;
  /** Disable uTP peer connections. */
  disableUTP?: boolean;
  /** Seed completed torrents after download. */
  seed?: boolean;
  /** Preferred local port. */
  listenPort?: number;
  /** Override the peer ID. */
  peerID?: string;
  /** Enable verbose debug logging. */
  debug?: boolean;

  /** Storage adapter. Defaults to in-memory. */
  storage?: StorageAdapter;

  /**
   * Node-compatible `net` module. Pass `(await import('@fkn/lib')).net`
   * in the browser, or omit to skip TCP entirely.
   */
  net?: NodeNetModule;

  /**
   * Node-compatible `dgram` module. Pass `(await import('@fkn/lib')).dgram`
   * in the browser. Required for the DHT, UDP trackers, and uTP.
   */
  dgram?: NodeDgramModule;
}
