// JS half of the WASM bridge.
//
// The Go runtime calls into `globalThis.__torrentBridge.<method>(...)`
// for every network and storage operation. This module implements that
// surface by driving Node-compatible `net` and `dgram` modules directly
// (typically the ones from @fkn/lib).

import type {
  NodeDgramModule,
  NodeDgramSocket,
  NodeNetModule,
  NodeServer,
  NodeSocket,
  StorageAdapter,
  StorageTorrent,
} from './types.js';

/** Shape of the global the Go runtime expects (globalThis.__torrentBridge). */
export interface TorrentBridge {
  onReady?: () => void;

  // Streams (TCP-like).
  dial(network: string, addr: string): Promise<{ id: number; localAddr: string; remoteAddr: string }>;
  connRead(id: number, maxLen: number): Promise<{ data: Uint8Array; eof?: boolean } | null>;
  connWrite(id: number, data: Uint8Array): Promise<{ n: number }>;
  connClose(id: number): Promise<void>;
  connSetDeadline(id: number, ms: number): Promise<void>;
  connSetReadDeadline(id: number, ms: number): Promise<void>;
  connSetWriteDeadline(id: number, ms: number): Promise<void>;

  // Listeners.
  listenTcp(network: string, addr: string): Promise<{ id: number; localAddr: string } | null>;
  listenerAccept(id: number): Promise<{ id: number; localAddr: string; remoteAddr: string } | null>;
  listenerClose(id: number): Promise<void>;

  // Packet sockets (UDP-like).
  packetListen(network: string, addr: string): Promise<{ id: number; localAddr: string }>;
  packetReadFrom(id: number): Promise<{ data: Uint8Array; addr: string } | null>;
  packetWriteTo(id: number, data: Uint8Array, addr: string): Promise<{ n: number }>;
  packetClose(id: number): Promise<void>;
  packetSetDeadline(id: number, ms: number): Promise<void>;
  packetSetReadDeadline(id: number, ms: number): Promise<void>;
  packetSetWriteDeadline(id: number, ms: number): Promise<void>;

  // uTP: tunnels through the host's packet socket. Default: outbound uTP
  // dials transparently fall back to plain TCP; inbound is unsupported.
  utpAccept(packetId: number): Promise<{ id: number; localAddr: string; remoteAddr: string } | null>;
  utpDial(packetId: number, addr: string): Promise<{ id: number; localAddr: string; remoteAddr: string }>;

  // Storage.
  storageOpen(
    infoHash: string,
    name: string,
    pieceLength: number,
    numPieces: number,
  ): Promise<{ id: number }>;
  storageTorrentClose(id: number): Promise<void>;
  storagePieceReadAt(
    torrentId: number,
    pieceIndex: number,
    off: number,
    len: number,
  ): Promise<{ data: Uint8Array }>;
  storagePieceWriteAt(
    torrentId: number,
    pieceIndex: number,
    off: number,
    data: Uint8Array,
  ): Promise<{ n: number }>;
  storagePieceMarkComplete(torrentId: number, pieceIndex: number): Promise<void>;
  storagePieceMarkNotComplete(torrentId: number, pieceIndex: number): Promise<void>;
  storagePieceCompletion(
    torrentId: number,
    pieceIndex: number,
  ): Promise<{ ok: boolean; complete: boolean }>;
}

// --------------------------------------------------------------------------
// Small helpers.

function parseAddr(addr: string): { host: string; port: number } {
  const i = addr.lastIndexOf(':');
  if (i < 0) throw new Error('invalid address: ' + addr);
  const host = addr.slice(0, i).replace(/^\[|\]$/g, '');
  const port = Number.parseInt(addr.slice(i + 1), 10);
  if (!Number.isFinite(port)) throw new Error('invalid port in: ' + addr);
  return { host, port };
}

function formatAddr(host: string | undefined, port: number | undefined): string {
  if (!host) host = '';
  if (port === undefined) port = 0;
  return host.includes(':') ? `[${host}]:${port}` : `${host}:${port}`;
}

class IdTable<T> {
  private next = 1;
  private map = new Map<number, T>();
  put(v: T): number {
    const id = this.next++;
    this.map.set(id, v);
    return id;
  }
  get(id: number): T | undefined {
    return this.map.get(id);
  }
  delete(id: number): void {
    this.map.delete(id);
  }
}

// --------------------------------------------------------------------------
// Stream/packet wrappers that translate the event-driven Node API into
// the Go-friendly promise-based bridge API.

class StreamState {
  private buf: Uint8Array[] = [];
  private waiters: Array<(v: Uint8Array | null) => void> = [];
  private ended = false;
  private err: Error | null = null;
  readonly localAddr: string;
  readonly remoteAddr: string;

  constructor(readonly sock: NodeSocket, localAddr: string, remoteAddr: string) {
    this.localAddr = localAddr;
    this.remoteAddr = remoteAddr;
    sock.on('data', (chunk) => {
      this.buf.push(chunk);
      this.flush();
    });
    sock.on('end', () => {
      this.ended = true;
      this.flush();
    });
    sock.on('close', () => {
      this.ended = true;
      this.flush();
    });
    sock.on('error', (e) => {
      this.err = e;
      this.flush();
    });
  }

  private flush(): void {
    while (this.waiters.length > 0 && (this.buf.length > 0 || this.ended || this.err)) {
      const w = this.waiters.shift()!;
      if (this.buf.length > 0) w(this.buf.shift()!);
      else w(null);
    }
  }

  read(maxLen: number): Promise<Uint8Array | null> {
    if (this.err) return Promise.reject(this.err);
    if (this.buf.length > 0) {
      const chunk = this.buf.shift()!;
      if (chunk.length <= maxLen) return Promise.resolve(chunk);
      this.buf.unshift(chunk.subarray(maxLen));
      return Promise.resolve(chunk.subarray(0, maxLen));
    }
    if (this.ended) return Promise.resolve(null);
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  write(data: Uint8Array): Promise<number> {
    return new Promise((resolve, reject) => {
      this.sock.write(data, (e?: Error) => {
        if (e) reject(e);
        else resolve(data.length);
      });
    });
  }

  close(): Promise<void> {
    this.sock.destroy();
    return Promise.resolve();
  }
}

class PacketState {
  private queue: Array<{ data: Uint8Array; addr: string }> = [];
  private waiters: Array<(v: { data: Uint8Array; addr: string } | null) => void> = [];
  private closed = false;
  readonly localAddr: string;

  constructor(readonly sock: NodeDgramSocket, localAddr: string) {
    this.localAddr = localAddr;
    sock.on('message', (msg, rinfo) => {
      const entry = { data: msg, addr: formatAddr(rinfo.address, rinfo.port) };
      const w = this.waiters.shift();
      if (w) w(entry);
      else this.queue.push(entry);
    });
    sock.on('error', () => {
      this.closed = true;
      while (this.waiters.length > 0) this.waiters.shift()!(null);
    });
  }

  readFrom(): Promise<{ data: Uint8Array; addr: string } | null> {
    if (this.queue.length > 0) return Promise.resolve(this.queue.shift()!);
    if (this.closed) return Promise.resolve(null);
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  writeTo(data: Uint8Array, addr: string): Promise<number> {
    const { host, port } = parseAddr(addr);
    return new Promise((resolve, reject) => {
      this.sock.send(data, port, host, (err, bytes) => {
        if (err) reject(err);
        else resolve(bytes ?? data.length);
      });
    });
  }

  close(): Promise<void> {
    this.closed = true;
    return new Promise((r) => this.sock.close(() => r()));
  }
}

class ListenerState {
  private pending: StreamState[] = [];
  private waiters: Array<(v: StreamState | null) => void> = [];
  private closed = false;
  readonly localAddr: string;

  constructor(readonly server: NodeServer, localAddr: string) {
    this.localAddr = localAddr;
    server.on('connection', (sock) => {
      const remote = formatAddr(sock.remoteAddress, sock.remotePort);
      const local = formatAddr(sock.localAddress, sock.localPort);
      const s = new StreamState(sock, local, remote);
      const w = this.waiters.shift();
      if (w) w(s);
      else this.pending.push(s);
    });
  }

  accept(): Promise<StreamState | null> {
    if (this.pending.length > 0) return Promise.resolve(this.pending.shift()!);
    if (this.closed) return Promise.resolve(null);
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  close(): Promise<void> {
    this.closed = true;
    return new Promise((r) => this.server.close(() => r()));
  }
}

// --------------------------------------------------------------------------
// Bridge construction.

export interface BridgeInputs {
  /** Node-compatible `net` (typically `(await import('@fkn/lib')).net`). */
  net?: NodeNetModule;
  /** Node-compatible `dgram` (typically `(await import('@fkn/lib')).dgram`). */
  dgram?: NodeDgramModule;
  storage: StorageAdapter;
  onReady?: () => void;
}

export function createBridge({ net, dgram, storage, onReady }: BridgeInputs): TorrentBridge {
  const streams = new IdTable<StreamState>();
  const listeners = new IdTable<ListenerState>();
  const packets = new IdTable<PacketState>();
  const storageTorrents = new IdTable<StorageTorrent>();

  function streamHandle(s: StreamState, id: number) {
    return { id, localAddr: s.localAddr, remoteAddr: s.remoteAddr };
  }

  function requireNet(): NodeNetModule {
    if (!net) throw new Error('no `net` module supplied to createBridge');
    return net;
  }
  function requireDgram(): NodeDgramModule {
    if (!dgram) throw new Error('no `dgram` module supplied to createBridge');
    return dgram;
  }

  const bridge: TorrentBridge = {
    ...(onReady !== undefined ? { onReady } : {}),

    async dial(_network, addr) {
      const { host, port } = parseAddr(addr);
      const sock = requireNet().createConnection({ host, port });
      await new Promise<void>((resolve, reject) => {
        sock.on('connect', () => resolve());
        sock.on('error', (e) => reject(e));
      });
      const local = formatAddr(sock.localAddress, sock.localPort);
      const remote = formatAddr(sock.remoteAddress ?? host, sock.remotePort ?? port);
      const s = new StreamState(sock, local, remote);
      const id = streams.put(s);
      return streamHandle(s, id);
    },
    async connRead(id, maxLen) {
      const s = streams.get(id);
      if (!s) return null;
      const data = await s.read(maxLen);
      if (data === null) return null;
      if (data.length === 0) return { data, eof: true };
      return { data };
    },
    async connWrite(id, data) {
      const s = streams.get(id);
      if (!s) throw new Error('connWrite: unknown stream id ' + id);
      return { n: await s.write(data) };
    },
    async connClose(id) {
      const s = streams.get(id);
      streams.delete(id);
      if (s) await s.close();
    },
    async connSetDeadline() {},
    async connSetReadDeadline() {},
    async connSetWriteDeadline() {},

    async listenTcp(_network, addr) {
      if (!net) return null;
      try {
        const server = net.createServer();
        const { host, port } = parseAddr(addr);
        await new Promise<void>((resolve, reject) => {
          server.on('listening', () => resolve());
          server.on('error', (e) => reject(e));
          server.listen(port, host);
        });
        const a = server.address();
        const local =
          typeof a === 'object' && a ? formatAddr(a.address, a.port) : addr;
        const l = new ListenerState(server, local);
        const id = listeners.put(l);
        return { id, localAddr: local };
      } catch {
        return null;
      }
    },
    async listenerAccept(id) {
      const l = listeners.get(id);
      if (!l) return null;
      const s = await l.accept();
      if (s === null) return null;
      const sid = streams.put(s);
      return streamHandle(s, sid);
    },
    async listenerClose(id) {
      const l = listeners.get(id);
      listeners.delete(id);
      if (l) await l.close();
    },

    async packetListen(network, addr) {
      const { host, port } = parseAddr(addr);
      const type = network === 'udp6' ? 'udp6' : 'udp4';
      const sock = requireDgram().createSocket(type);
      await new Promise<void>((resolve, reject) => {
        sock.on('listening', () => resolve());
        sock.on('error', (e) => reject(e));
        sock.bind(port, host);
      });
      const a = sock.address();
      const local = formatAddr(a.address, a.port);
      const p = new PacketState(sock, local);
      const id = packets.put(p);
      return { id, localAddr: local };
    },
    async packetReadFrom(id) {
      const p = packets.get(id);
      if (!p) return null;
      return await p.readFrom();
    },
    async packetWriteTo(id, data, addr) {
      const p = packets.get(id);
      if (!p) throw new Error('packetWriteTo: unknown id ' + id);
      return { n: await p.writeTo(data, addr) };
    },
    async packetClose(id) {
      const p = packets.get(id);
      packets.delete(id);
      if (p) await p.close();
    },
    async packetSetDeadline() {},
    async packetSetReadDeadline() {},
    async packetSetWriteDeadline() {},

    async utpAccept() {
      // Inbound uTP isn't implemented in the default bridge. A host may
      // override this method post-construction.
      return null;
    },
    async utpDial(_packetId, addr) {
      // Outbound uTP falls back to plain TCP if `net` is available.
      const { host, port } = parseAddr(addr);
      const sock = requireNet().createConnection({ host, port });
      await new Promise<void>((resolve, reject) => {
        sock.on('connect', () => resolve());
        sock.on('error', (e) => reject(e));
      });
      const local = formatAddr(sock.localAddress, sock.localPort);
      const remote = formatAddr(sock.remoteAddress ?? host, sock.remotePort ?? port);
      const s = new StreamState(sock, local, remote);
      const id = streams.put(s);
      return streamHandle(s, id);
    },

    async storageOpen(infoHash, name, pieceLength, numPieces) {
      const st = await storage.open({ infoHash, name, pieceLength, numPieces });
      const id = storageTorrents.put(st);
      return { id };
    },
    async storageTorrentClose(id) {
      const st = storageTorrents.get(id);
      storageTorrents.delete(id);
      if (st) await st.close();
    },
    async storagePieceReadAt(torrentId, pieceIndex, off, len) {
      const st = storageTorrents.get(torrentId);
      if (!st) throw new Error('storagePieceReadAt: unknown torrent ' + torrentId);
      return { data: await st.pieceReadAt(pieceIndex, off, len) };
    },
    async storagePieceWriteAt(torrentId, pieceIndex, off, data) {
      const st = storageTorrents.get(torrentId);
      if (!st) throw new Error('storagePieceWriteAt: unknown torrent ' + torrentId);
      return { n: await st.pieceWriteAt(pieceIndex, off, data) };
    },
    async storagePieceMarkComplete(torrentId, pieceIndex) {
      const st = storageTorrents.get(torrentId);
      if (st) await st.pieceMarkComplete(pieceIndex);
    },
    async storagePieceMarkNotComplete(torrentId, pieceIndex) {
      const st = storageTorrents.get(torrentId);
      if (st) await st.pieceMarkNotComplete(pieceIndex);
    },
    async storagePieceCompletion(torrentId, pieceIndex) {
      const st = storageTorrents.get(torrentId);
      if (!st) return { ok: false, complete: false };
      return await st.pieceCompletion(pieceIndex);
    },
  };

  return bridge;
}
