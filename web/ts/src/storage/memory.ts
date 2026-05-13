// In-memory storage adapter. Useful for streaming/ephemeral workloads
// where torrent data does not need to survive a page reload.

import type {
  StorageAdapter,
  StorageTorrent,
  StorageTorrentOpenInfo,
} from '../types.js';

class MemoryStorageTorrent implements StorageTorrent {
  private readonly pieces: Map<number, Uint8Array> = new Map();
  private readonly complete: Set<number> = new Set();
  private readonly pieceLength: number;
  private readonly numPieces: number;

  constructor(info: StorageTorrentOpenInfo) {
    this.pieceLength = info.pieceLength;
    this.numPieces = info.numPieces;
  }

  private buf(index: number): Uint8Array {
    let b = this.pieces.get(index);
    if (!b) {
      b = new Uint8Array(this.pieceLength);
      this.pieces.set(index, b);
    }
    return b;
  }

  async pieceReadAt(pieceIndex: number, offset: number, length: number): Promise<Uint8Array> {
    const b = this.pieces.get(pieceIndex);
    if (!b) return new Uint8Array(0);
    return b.subarray(offset, offset + length);
  }

  async pieceWriteAt(pieceIndex: number, offset: number, data: Uint8Array): Promise<number> {
    const b = this.buf(pieceIndex);
    b.set(data, offset);
    // Eagerly compact: if the write extended beyond pieceLength we'd
    // panic Go-side. Trust the runtime.
    return data.length;
  }

  async pieceMarkComplete(pieceIndex: number): Promise<void> {
    this.complete.add(pieceIndex);
  }

  async pieceMarkNotComplete(pieceIndex: number): Promise<void> {
    this.complete.delete(pieceIndex);
  }

  async pieceCompletion(pieceIndex: number): Promise<{ ok: boolean; complete: boolean }> {
    return { ok: true, complete: this.complete.has(pieceIndex) };
  }

  async close(): Promise<void> {
    this.pieces.clear();
    this.complete.clear();
  }
}

/** In-memory storage. All data lives in JS heap and is discarded on close. */
export function memoryStorage(): StorageAdapter {
  return {
    async open(info) {
      return new MemoryStorageTorrent(info);
    },
  };
}
