# `@anacrolix/torrent` (browser TypeScript wrapper)

This directory contains a TypeScript / WebAssembly distribution of
[`anacrolix/torrent`][upstream] for the browser. The Go library is
compiled to `GOOS=js GOARCH=wasm` and driven from TypeScript over a
small bridge installed at `globalThis.__torrentBridge`.

[upstream]: https://github.com/anacrolix/torrent

> **Status:** alpha. The bridge and storage layers compile and are
> structurally complete, but full peer connectivity in the browser
> depends on the host environment supplying a working `NetAdapter`. See
> [Network adapter](#network-adapter) below.

## Layout

```
web/
├── build-wasm.sh         # Builds web/dist/torrent.wasm + wasm_exec.js
├── jsbridge/             # Go-side bridge: net.Conn / net.PacketConn /
│                         #   net.Listener / storage.ClientImpl
│                         #   implementations that defer to JS.
├── wasm/                 # GOOS=js entry point. Exposes the WASM API on
│                         #   globalThis.__torrent via syscall/js.
└── ts/                   # The npm package source.
    └── src/
        ├── index.ts      # Public API (Client/Torrent/File).
        ├── bridge.ts     # JS side of the WASM bridge.
        ├── wasm.ts       # Loads wasm_exec.js + the .wasm artifact.
        ├── types.ts      # Public adapter interfaces.
        ├── net/
        │   └── fkn.ts    # NetAdapter backed by @fkn/lib (TCP + UDP).
        └── storage/
            ├── memory.ts # In-memory StorageAdapter.
            └── opfs.ts   # Origin Private FS StorageAdapter.
```

## Building

```bash
# 1) Build the WASM artifact (writes web/dist/{torrent.wasm,wasm_exec.js}).
./web/build-wasm.sh

# 2) Build the TS package.
cd web/ts
npm install
npm run build
```

## Usage

```ts
import { createClient, opfsStorage } from '@anacrolix/torrent';
import * as fkn from '@fkn/lib';

const client = await createClient({
  wasmUrl: new URL('@anacrolix/torrent/wasm', import.meta.url),
  net: fkn.net,     // Node-compatible `net` polyfill from @fkn/lib
  dgram: fkn.dgram, // Node-compatible `dgram` polyfill from @fkn/lib
  storage: await opfsStorage(),
});

const torrent = await client.addMagnet(
  'magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a',
);

const info = await torrent.gotInfo();
const [file] = await torrent.files();

// Stream a file:
const stream = file.stream();
// Or read a slice:
const bytes = await file.read(0, 1 << 20);
```

`net` and `dgram` are accepted as Node-compatible modules — they're
passed straight through to the bridge, which drives them with the same
calls Node code would (`net.createConnection`, `dgram.createSocket`,
etc.). Anything with that surface works: @fkn/lib, the real Node
modules (for server-side testing), or your own implementation.

## Architecture

```
                ┌──────────────────────────────────────────┐
                │       TypeScript application code        │
                └─────────────────────┬────────────────────┘
                                      │
                          @anacrolix/torrent (this package)
                                      │
       ┌──────────────────────────────┴──────────────────────────────┐
       │                                                              │
       │  globalThis.__torrent        ◄── exposed by Go via syscall/js
       │      .newClient / .addMagnet / ... (Promises)                │
       │                                                              │
       │  globalThis.__torrentBridge  ◄── installed by TS, called by Go
       │      .dial / .connRead / .connWrite / .connClose             │
       │      .packetListen / .packetReadFrom / .packetWriteTo        │
       │      .storageOpen / .storagePieceReadAt / ...                │
       │                                                              │
       └─────────────────────┬────────────────────────────────────────┘
                             │
              ┌──────────────┴──────────────┐
              │                             │
        ┌─────▼─────┐               ┌───────▼──────┐
        │NetAdapter │               │StorageAdapter│
        │@fkn/lib   │               │OPFS / memory │
        └───────────┘               └──────────────┘
```

The Go runtime never makes real TCP/UDP syscalls (those don't exist in
`GOOS=js`). Instead, the small `web/jsbridge` package implements
`net.Conn`, `net.PacketConn`, `net.Listener`, and `storage.ClientImpl`
in terms of `syscall/js` calls back into the host. The host is free to
implement those calls against `@fkn/lib`, Chrome's experimental Direct
Sockets, an in-page WebRTC relay, etc.

## Network modules

`createClient` accepts two Node-compatible modules directly:

- `net` — used for outbound TCP peer connections and (optionally) inbound
  listeners. Skipped when omitted (and `disableTCP` is set on the Go
  side automatically).
- `dgram` — used for UDP packet sockets, which the DHT, UDP trackers,
  and uTP all run on. Skipped when omitted (`disableDHT` and
  `disableUTP` are set automatically).

In the browser, pass `@fkn/lib`'s implementations:

```ts
import * as fkn from '@fkn/lib';
await createClient({ /* ... */ net: fkn.net, dgram: fkn.dgram });
```

Anything with the same shape works — the bridge drives them with the
plain Node API (`createConnection`, `createSocket`, `.on('data', ...)`,
`.send(...)`, etc.). No additional wrapper layer is needed.

## Transport coverage

When @fkn/lib's `net` and `dgram` polyfills are used, every BitTorrent
transport works the same as the native Go build:

- **Outbound TCP peers** — `net.createConnection` via the bridge.
- **Inbound TCP peers** — `net.createServer` via the bridge.
- **UDP trackers** — `cfg.TrackerListenPacket` is set to the bridge's
  packet listener.
- **uTP** — driven by the real `anacrolix/utp` Go implementation
  (build tag `-tags disable_libutp`), running on top of a JS-backed
  `net.PacketConn`. All framing happens in Go.
- **DHT** — same UDP path as UDP trackers.
- **HTTP trackers / webseeds** — `cfg.HTTPDialContext` and
  `cfg.TrackerDialContext` go through the bridge's TCP dial, so HTTP
  requests skip the browser `fetch` path and are not subject to CORS.

## Limitations

1. **Workers**: this build is single-threaded by request — all bridge
   methods (including storage reads) run on the main thread.
2. **Filesystem layout**: OPFS only — there is no equivalent of the Go
   "file" backend that writes the original directory layout to disk.

## Modifications to the upstream library

The bridge needs two hooks in the otherwise-untouched upstream code:

- `socket.go` exports `Socket` (a type alias for the internal
  `socket` interface) and two package-level vars,
  `JsBridgeListenTcp` and `JsBridgeListenUtp`. When non-nil they are
  used instead of the standard `net.ListenConfig` / `NewUtpSocket`
  paths. The WASM entry point (`web/wasm/main.go`) installs them via
  `jsbridge.Install()`.

That's it — no other upstream Go files are touched.
