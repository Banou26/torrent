// OPFS (Origin Private File System) storage adapter.
//
// Each torrent is mapped to a directory in OPFS, and each piece to a
// single file inside that directory. A separate `.complete` zero-byte
// file marks a piece as verified by the BitTorrent engine.
//
// This is intentionally simple - it does not interleave file layouts the
// way the Go "file" backend does - because we run on the main thread
// without access to sync access handles. (Sync access handles, which
// would let us back the torrent's exact on-disk layout efficiently, are
// only available inside dedicated workers.)

import type {
  StorageAdapter,
  StorageTorrent,
  StorageTorrentOpenInfo,
} from '../types.js';

async function getOpfsRoot(): Promise<FileSystemDirectoryHandle> {
  if (typeof navigator === 'undefined' || !navigator.storage?.getDirectory) {
    throw new Error('OPFS is not available in this environment');
  }
  return await navigator.storage.getDirectory();
}

async function getOrCreateDir(
  parent: FileSystemDirectoryHandle,
  name: string,
): Promise<FileSystemDirectoryHandle> {
  return await parent.getDirectoryHandle(name, { create: true });
}

async function readFileBytes(
  dir: FileSystemDirectoryHandle,
  name: string,
): Promise<Uint8Array | null> {
  try {
    const h = await dir.getFileHandle(name);
    const f = await h.getFile();
    return new Uint8Array(await f.arrayBuffer());
  } catch (err) {
    if ((err as DOMException).name === 'NotFoundError') return null;
    throw err;
  }
}

async function writeFileBytes(
  dir: FileSystemDirectoryHandle,
  name: string,
  data: Uint8Array,
): Promise<void> {
  const h = await dir.getFileHandle(name, { create: true });
  const w = await h.createWritable();
  // The OPFS WritableStream insists on an ArrayBuffer-backed view. Make
  // a fresh ArrayBuffer copy regardless of the source's buffer flavour
  // (the TS DOM lib excludes SharedArrayBuffer-backed views).
  const copy = new Uint8Array(new ArrayBuffer(data.byteLength));
  copy.set(data);
  await w.write(copy);
  await w.close();
}

class OpfsStorageTorrent implements StorageTorrent {
  private constructor(
    private readonly dir: FileSystemDirectoryHandle,
    private readonly pieceLength: number,
  ) {}

  static async open(
    root: FileSystemDirectoryHandle,
    info: StorageTorrentOpenInfo,
  ): Promise<OpfsStorageTorrent> {
    const dir = await getOrCreateDir(root, info.infoHash);
    return new OpfsStorageTorrent(dir, info.pieceLength);
  }

  private pieceName(index: number): string {
    return `p${index}.bin`;
  }

  private completeName(index: number): string {
    return `p${index}.complete`;
  }

  async pieceReadAt(pieceIndex: number, offset: number, length: number): Promise<Uint8Array> {
    const data = await readFileBytes(this.dir, this.pieceName(pieceIndex));
    if (!data) return new Uint8Array(0);
    return data.subarray(offset, offset + length);
  }

  async pieceWriteAt(pieceIndex: number, offset: number, data: Uint8Array): Promise<number> {
    const existing = (await readFileBytes(this.dir, this.pieceName(pieceIndex))) ??
      new Uint8Array(this.pieceLength);
    // Ensure buffer is at least pieceLength.
    const buf =
      existing.length >= this.pieceLength
        ? existing
        : new Uint8Array(this.pieceLength);
    if (existing !== buf) buf.set(existing);
    buf.set(data, offset);
    await writeFileBytes(this.dir, this.pieceName(pieceIndex), buf);
    return data.length;
  }

  async pieceMarkComplete(pieceIndex: number): Promise<void> {
    const h = await this.dir.getFileHandle(this.completeName(pieceIndex), { create: true });
    const w = await h.createWritable();
    await w.close();
  }

  async pieceMarkNotComplete(pieceIndex: number): Promise<void> {
    try {
      await this.dir.removeEntry(this.completeName(pieceIndex));
    } catch (err) {
      if ((err as DOMException).name !== 'NotFoundError') throw err;
    }
  }

  async pieceCompletion(pieceIndex: number): Promise<{ ok: boolean; complete: boolean }> {
    try {
      await this.dir.getFileHandle(this.completeName(pieceIndex));
      return { ok: true, complete: true };
    } catch (err) {
      if ((err as DOMException).name === 'NotFoundError') return { ok: true, complete: false };
      return { ok: false, complete: false };
    }
  }

  async close(): Promise<void> {
    // OPFS handles do not need closing.
  }
}

export interface OpfsStorageOptions {
  /** Subdirectory of OPFS to use. Defaults to "torrents". */
  rootName?: string;
}

export async function opfsStorage(opts: OpfsStorageOptions = {}): Promise<StorageAdapter> {
  const rootName = opts.rootName ?? 'torrents';
  const opfsRoot = await getOpfsRoot();
  const root = await getOrCreateDir(opfsRoot, rootName);
  return {
    async open(info) {
      return await OpfsStorageTorrent.open(root, info);
    },
  };
}
