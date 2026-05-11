import type {
  ClientHandle,
  CreateClientOptions,
  TorrentHandle,
  TorrentHost,
} from './types.js'

export * from './types.js'

declare global {
  // Set by wasm_exec.js. Defining here lets us call new Go() and run() without
  // shipping the upstream types.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  var Go: { new (): { importObject: any; run: (instance: WebAssembly.Instance) => Promise<void> } }
  // The wasm binary calls this on startup; we use it to know the module is up.
  var __torrent_on_ready: (() => void) | undefined
  // Installed by the wasm binary after main() runs.
  var __torrent: {
    createClient: (opts: CreateClientOptions) => Promise<ClientHandle>
  } | undefined
  // The host networking adapter — we install it before run().
  var __torrent_host: TorrentHost | undefined
}

export type LoadOptions = {
  /** URL or path of the torrent.wasm binary. */
  wasm: string | URL | Response | ArrayBuffer | WebAssembly.Module
  /** URL or path of Go's wasm_exec.js. Required if not already loaded. */
  wasmExec?: string | URL
  /** Host that fulfils the networking contract. See ./host-fkn for an @fkn/lib adapter. */
  host: TorrentHost
}

let loadPromise: Promise<void> | null = null

/**
 * Loads the torrent WASM module and installs the host bridge. The returned
 * promise resolves when Go's main has installed globalThis.__torrent. Calling
 * load() twice is a no-op.
 */
export const load = async (opts: LoadOptions): Promise<void> => {
  if (loadPromise) return loadPromise

  loadPromise = (async () => {
    if (typeof globalThis.Go === 'undefined') {
      if (!opts.wasmExec) {
        throw new Error('torrent-wasm: globalThis.Go is not defined. Pass options.wasmExec to load it.')
      }
      await loadWasmExec(opts.wasmExec)
      if (typeof globalThis.Go === 'undefined') {
        throw new Error('torrent-wasm: wasm_exec.js did not install globalThis.Go')
      }
    }
    globalThis.__torrent_host = opts.host
    const ready = new Promise<void>((resolve) => {
      globalThis.__torrent_on_ready = () => resolve()
    })
    const go = new globalThis.Go()
    const wasmModule = await instantiateWasm(opts.wasm, go.importObject)
    // Don't await go.run() — it never resolves while main is select{}-blocked.
    void go.run(wasmModule.instance)
    await ready
  })()

  return loadPromise
}

// wasm_exec.js is a classic IIFE that mutates globalThis. We can't import() it
// as ESM; the cleanest cross-environment path is to fetch the source and
// evaluate it via a Function constructor that aliases globalThis.
const loadWasmExec = async (src: string | URL): Promise<void> => {
  const url = src instanceof URL ? src.href : src
  const text = await (await fetch(url)).text()
  // Indirect eval keeps it out of the module scope; the script writes
  // globalThis.Go regardless.
  // eslint-disable-next-line no-new-func
  new Function(text)()
}

const instantiateWasm = async (
  source: LoadOptions['wasm'],
  importObject: WebAssembly.Imports,
): Promise<WebAssembly.WebAssemblyInstantiatedSource> => {
  if (source instanceof WebAssembly.Module) {
    const instance = await WebAssembly.instantiate(source, importObject)
    return { module: source, instance }
  }
  if (source instanceof ArrayBuffer) {
    return WebAssembly.instantiate(source, importObject)
  }
  let response: Response
  if (source instanceof Response) {
    response = source
  } else {
    response = await fetch(source instanceof URL ? source.href : source)
    if (!response.ok) {
      throw new Error(`torrent-wasm: failed to fetch wasm: ${response.status} ${response.statusText}`)
    }
  }
  if (typeof WebAssembly.instantiateStreaming === 'function') {
    return WebAssembly.instantiateStreaming(response, importObject)
  }
  const buf = await response.arrayBuffer()
  return WebAssembly.instantiate(buf, importObject)
}

/**
 * Create a new torrent client. load() must have completed first.
 */
export const createClient = async (opts: CreateClientOptions = {}): Promise<ClientHandle> => {
  if (typeof globalThis.__torrent === 'undefined') {
    throw new Error('torrent-wasm: call load() before createClient()')
  }
  const handle = await globalThis.__torrent.createClient(opts)
  return wrapClientHandle(handle)
}

const wrapClientHandle = (raw: ClientHandle): ClientHandle => {
  const wrapTorrent = (rawT: TorrentHandle): TorrentHandle => ({
    get infoHash() {
      return rawT.infoHash
    },
    get name() {
      return rawT.name
    },
    gotInfo: () => rawT.gotInfo(),
    info: () => rawT.info(),
    files: () => rawT.files(),
    downloadAll: () => rawT.downloadAll(),
    stats: () => rawT.stats(),
    bytesCompleted: () => rawT.bytesCompleted(),
    drop: () => rawT.drop(),
  })

  return {
    get peerId() {
      return raw.peerId
    },
    addMagnet: async (uri) => wrapTorrent(await raw.addMagnet(uri)),
    addInfoHash: async (hex) => wrapTorrent(await raw.addInfoHash(hex)),
    torrents: () => raw.torrents().map(wrapTorrent),
    listenAddrs: () => raw.listenAddrs(),
    close: () => raw.close(),
  }
}
