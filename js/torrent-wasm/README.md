# @anacrolix/torrent-wasm

`anacrolix/torrent` — a full-featured BitTorrent client — compiled to
WebAssembly with a small JS API. All peer traffic (TCP, uTP, DHT, UDP
trackers) is tunnelled through a pluggable JS host. HTTP trackers and
webseeds use Go's `net/http` Transport, also routed through the same host so
the browser's CORS rules don't apply.

The default-recommended host adapter targets the FKN WebVPN
(`@fkn/lib`), which gives you real TCP/UDP sockets over WebTransport. You
can also bring your own host implementation.

## Install

```sh
npm install @anacrolix/torrent-wasm
```

The package ships a prebuilt `wasm/torrent.wasm` (~34 MB unstripped, gzip ~9
MB) plus Go's `wasm_exec.js` runtime.

## Quick start (with FKN WebVPN)

```ts
import { load, createClient } from '@anacrolix/torrent-wasm'
import { fknHost } from '@anacrolix/torrent-wasm/host-fkn'
// Or, if your @fkn/lib build only exposes the Node-shaped polyfills:
// import { fknPolyfillHost } from '@anacrolix/torrent-wasm/host-fkn-polyfill'
// import * as fkn from '@fkn/lib'

const wasmUrl  = new URL('@anacrolix/torrent-wasm/wasm/torrent.wasm', import.meta.url)
const execUrl  = new URL('@anacrolix/torrent-wasm/wasm/wasm_exec.js', import.meta.url)

await load({
  wasm: wasmUrl,
  wasmExec: execUrl,
  host: await fknHost(),                    // raw-streams adapter
  // host: fknPolyfillHost(fkn),            // Node-shaped fallback
})

const client = await createClient({
  storage: 'memory',                        // or 'opfs' with opfsRoot
})

const t = await client.addMagnet('magnet:?xt=urn:btih:...')
await t.gotInfo()
console.log(t.info())                       // { name, pieceLength, totalLength, numPieces }

// Stream a file to a <video>:
const file = t.files()[0]
const stream = file.createReadStream()      // ReadableStream<Uint8Array>
const url = URL.createObjectURL(await new Response(stream).blob())
videoEl.src = url
```

## Storage

Two backends are bundled:

- `storage: 'memory'` (default) — pieces are kept in RAM, freed on Client
  close. Best for streaming use cases.
- `storage: 'opfs'` — pieces are written to the Origin Private File System
  via a `FileSystemDirectoryHandle` you pass in `opfsRoot`. Each torrent gets
  a subdirectory; data persists across reloads.

```ts
const opfsRoot = await navigator.storage.getDirectory()
const client = await createClient({ storage: 'opfs', opfsRoot })
```

You can pass any custom storage by writing a Go-side `storage.ClientImpl` and
linking it into a fork of `cmd/torrent-wasm`. The bundled WASM only knows
about `memory` and `opfs`.

## Host contract

The WASM expects a host object on `globalThis.__torrent_host` (installed for
you by `load()`):

```ts
type TorrentHost = {
  dialTcp:   (o: { network: 'tcp4'|'tcp6'; address: string; port: number }) => Promise<TcpConn>
  listenTcp: (o: { network: 'tcp4'|'tcp6'; address: string; port: number }) => Promise<TcpListener>
  bindUdp:   (o: { network: 'udp4'|'udp6'; address: string; port: number }) => Promise<UdpSocket>
}

type TcpConn = {
  localAddress: string; localFamily: 'IPv4'|'IPv6'; localPort: number
  remoteAddress: string; remoteFamily: 'IPv4'|'IPv6'; remotePort: number
  dataReadableStream: ReadableStream<Uint8Array>
  dataWritableStream: WritableStream<Uint8Array>
  close: () => Promise<void>
}

type TcpListener = {
  localAddress: string; localFamily: 'IPv4'|'IPv6'; localPort: number
  accept: () => Promise<TcpConn | null>     // null on close
  close: () => Promise<void>
}

type UdpSocket = {
  localAddress: string; localFamily: 'IPv4'|'IPv6'; localPort: number
  dataReadableStream: ReadableStream<{ data: ArrayBuffer; address: string; port: number; family: 'IPv4'|'IPv6' }>
  send: (o: { message: ArrayBuffer; address: string; port: number }) => Promise<void>
  close: () => Promise<void>
}
```

Implement these three functions over any transport you like (raw WebSockets,
WebTransport, WebRTC datachannels back-haul, your own VPN, etc) and the
torrent client will work with no further wiring.

## What works

| Feature                       | Status                                            |
|-------------------------------|---------------------------------------------------|
| TCP peer connections          | Through the host's `dialTcp` / `listenTcp`        |
| uTP peer connections          | Pure-Go `anacrolix/utp` over the host's `bindUdp` |
| DHT (mainline)                | Over the host's `bindUdp`                         |
| UDP trackers                  | Over the host's `bindUdp`                         |
| HTTP & HTTPS trackers         | Go's `http.Transport` → host's `dialTcp` (CORS-free) |
| Webseeds (HTTP)               | Same as HTTP trackers                             |
| WebTorrent (WebRTC) peers     | `pion/webrtc/v4` JS build, talks to native browser WebRTC |
| BEP-9 metadata over magnet    | Yes                                               |
| BEP-11 PEX                    | Yes                                               |
| MSE / RC4 protocol encryption | Yes                                               |
| BEP-14 Local Service Discovery | Disabled in browser (no multicast)               |
| Streaming (`createReadStream`)| Yes — seeks trigger on-demand piece prioritisation |

## Build from source

```sh
cd js/torrent-wasm
npm install
npm run build         # builds wasm and TS in one go
```

`build:wasm` invokes `go build` with `GOOS=js GOARCH=wasm CGO_ENABLED=0`. The
output goes to `wasm/torrent.wasm`. `wasm_exec.js` is copied from `$GOROOT`.
