import { css } from '@emotion/react';
import { useEffect, useState } from 'preact/hooks';

import {
  createClient,
  memoryStorage,
  type Client,
  type Torrent,
  type File as TorrentFile,
  type TorrentInfo,
  type TorrentStats,
} from '@anacrolix/torrent';

import { net, dgram } from '@fkn/lib';

// Pre-built artifacts from the @anacrolix/torrent package. Vite resolves
// the file: dependency to its dist/.
const wasmUrl = new URL('@anacrolix/torrent/wasm', import.meta.url);
const wasmExecUrl = new URL('@anacrolix/torrent/wasm_exec', import.meta.url);

const styles = {
  page: css`
    font-family: ui-monospace, monospace;
    color: #d4d4d4;
    background: #1e1e1e;
    min-height: 100vh;
    padding: 24px;
    box-sizing: border-box;
  `,
  header: css`
    font-size: 16px;
    margin: 0 0 16px;
  `,
  status: css`
    padding: 8px 12px;
    border-radius: 4px;
    background: #2d2d2d;
    margin-bottom: 16px;
    font-size: 13px;
  `,
  row: css`
    display: flex;
    gap: 8px;
    margin-bottom: 16px;
  `,
  input: css`
    flex: 1;
    padding: 8px 12px;
    background: #2d2d2d;
    color: inherit;
    border: 1px solid #3a3a3a;
    border-radius: 4px;
    font: inherit;
  `,
  button: css`
    padding: 8px 16px;
    background: #0e639c;
    color: white;
    border: none;
    border-radius: 4px;
    font: inherit;
    cursor: pointer;
    &:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }
    &:hover:not(:disabled) {
      background: #1177bb;
    }
  `,
  panel: css`
    background: #252525;
    border-radius: 4px;
    padding: 16px;
    margin-bottom: 16px;
    font-size: 13px;
  `,
  fileRow: css`
    display: flex;
    justify-content: space-between;
    padding: 4px 0;
    border-bottom: 1px solid #2d2d2d;
    &:last-child {
      border-bottom: none;
    }
  `,
  filePath: css`
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    margin-right: 16px;
  `,
  fileSize: css`
    color: #999;
    flex-shrink: 0;
  `,
  err: css`
    color: #f48771;
  `,
};

const SINTEL =
  'magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10' +
  '&dn=Sintel' +
  '&tr=udp%3A%2F%2Fexplodie.org%3A6969' +
  '&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969' +
  '&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337' +
  '&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337';

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

type Status =
  | { kind: 'init' }
  | { kind: 'wasm-loading' }
  | { kind: 'ready' }
  | { kind: 'error'; message: string };

export const App = () => {
  const [status, setStatus] = useState<Status>({ kind: 'init' });
  const [client, setClient] = useState<Client | null>(null);
  const [magnetInput, setMagnetInput] = useState(SINTEL);
  const [torrent, setTorrent] = useState<Torrent | null>(null);
  const [info, setInfo] = useState<TorrentInfo | null>(null);
  const [files, setFiles] = useState<TorrentFile[]>([]);
  const [stats, setStats] = useState<TorrentStats | null>(null);
  const [busy, setBusy] = useState(false);

  // Boot the WASM client once. @fkn/lib's `net` / `dgram` modules tunnel
  // through WebTransport to the configured webvpn server.
  useEffect(() => {
    (async () => {
      try {
        setStatus({ kind: 'wasm-loading' });
        const cl = await createClient({
          wasmUrl,
          wasmExecUrl,
          net,
          dgram,
          storage: memoryStorage(),
        });
        setClient(cl);
        setStatus({ kind: 'ready' });
      } catch (err) {
        console.error('[app] init error', err);
        setStatus({ kind: 'error', message: (err as Error).message });
      }
    })();
  }, []);

  // Poll stats while a torrent is active.
  useEffect(() => {
    if (!torrent) return;
    let cancelled = false;
    const tick = async () => {
      try {
        const s = await torrent.stats();
        if (!cancelled) setStats(s);
      } catch {}
      if (!cancelled) setTimeout(tick, 1000);
    };
    tick();
    return () => {
      cancelled = true;
    };
  }, [torrent]);

  const addMagnet = async () => {
    if (!client || busy) return;
    setBusy(true);
    setInfo(null);
    setFiles([]);
    setStats(null);
    try {
      const t = await client.addMagnet(magnetInput);
      setTorrent(t);
      const i = await t.gotInfo();
      setInfo(i);
      const fs = await t.files();
      setFiles(fs);
    } catch (err) {
      setStatus({ kind: 'error', message: (err as Error).message });
    } finally {
      setBusy(false);
    }
  };

  const statusLabel = (() => {
    switch (status.kind) {
      case 'init':
        return 'initializing…';
      case 'wasm-loading':
        return 'loading wasm module (≈34 MB)…';
      case 'ready':
        return 'ready';
      case 'error':
        return `error: ${status.message}`;
    }
  })();

  return (
    <div css={styles.page}>
      <h1 css={styles.header}>@anacrolix/torrent — browser demo</h1>
      <div css={[styles.status, status.kind === 'error' && styles.err]}>{statusLabel}</div>

      <div css={styles.row}>
        <input
          css={styles.input}
          type="text"
          value={magnetInput}
          onInput={(e) => setMagnetInput((e.target as HTMLInputElement).value)}
          placeholder="magnet:?xt=urn:btih:…"
        />
        <button
          css={styles.button}
          onClick={addMagnet}
          disabled={status.kind !== 'ready' || busy}
        >
          {busy ? 'fetching info…' : 'add magnet'}
        </button>
      </div>

      {info && (
        <div css={styles.panel}>
          <div>
            <strong>{info.name}</strong>
          </div>
          <div>infoHash: {info.infoHash}</div>
          <div>
            length: {fmtBytes(info.length)} · pieces: {info.numPieces} ×{' '}
            {fmtBytes(info.pieceLength)}
          </div>
        </div>
      )}

      {files.length > 0 && (
        <div css={styles.panel}>
          <div style={{ marginBottom: 8 }}>files ({files.length}):</div>
          {files.map((f, i) => (
            <div key={i} css={styles.fileRow}>
              <span css={styles.filePath}>{f.path}</span>
              <span css={styles.fileSize}>{fmtBytes(f.length)}</span>
            </div>
          ))}
        </div>
      )}

      {stats && (
        <div css={styles.panel}>
          <div>peers: active {stats.activePeers} / seeders {stats.connectedSeeders} / known{' '}
            {stats.totalPeers}
          </div>
          <div>
            downloaded: {fmtBytes(stats.bytesCompleted)} / {fmtBytes(stats.bytesCompleted + stats.bytesMissing)}
          </div>
        </div>
      )}
    </div>
  );
};
