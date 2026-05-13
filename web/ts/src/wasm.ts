// Loads the Go WASM module.
//
// We require Go's wasm_exec.js runtime. It is shipped as a peer artifact
// next to the .wasm file (see ../build-wasm.sh). At runtime we import a
// dynamic ESM shim that wraps the classic wasm_exec.js script and
// exposes its global `Go` constructor.

declare global {
  // eslint-disable-next-line no-var
  var Go: undefined | { new (): GoRuntime };
}

interface GoRuntime {
  importObject: WebAssembly.Imports;
  argv: string[];
  env: Record<string, string>;
  run(instance: WebAssembly.Instance): Promise<void>;
  exit(code: number): void;
}

let wasmExecLoaded = false;

async function ensureWasmExec(wasmExecUrl: string | URL): Promise<void> {
  if (wasmExecLoaded || typeof globalThis.Go === 'function') {
    wasmExecLoaded = true;
    return;
  }
  // wasm_exec.js is shipped as a classic script by the Go toolchain. We
  // fetch it and evaluate it via Function() so the host doesn't have to
  // include it manually. The toolchain provides an ESM-compatible
  // variant from Go 1.23+ (`wasm_exec.js` is still a classic script as
  // of Go 1.24; this loader bridges either form).
  const resp = await fetch(wasmExecUrl.toString());
  if (!resp.ok) {
    throw new Error(`failed to fetch wasm_exec.js: ${resp.status} ${resp.statusText}`);
  }
  const src = await resp.text();
  // eslint-disable-next-line @typescript-eslint/no-implied-eval, no-new-func
  new Function(src).call(globalThis);
  if (typeof globalThis.Go !== 'function') {
    throw new Error('wasm_exec.js did not install globalThis.Go');
  }
  wasmExecLoaded = true;
}

export interface LoadOptions {
  /** URL or absolute path to the built torrent.wasm artifact. */
  wasmUrl: string | URL;
  /** URL to Go's wasm_exec.js runtime. Defaults to wasmUrl with the suffix replaced. */
  wasmExecUrl?: string | URL;
  /** Optional argv/env. */
  argv?: string[];
  env?: Record<string, string>;
}

/**
 * Instantiate the Go WASM module. The Promise resolves once the module
 * has signalled readiness (via __torrentBridge.onReady). The Go program
 * keeps running for the lifetime of the page.
 */
export async function loadWasm(opts: LoadOptions, readySignal: Promise<void>): Promise<void> {
  const wasmExecUrl = opts.wasmExecUrl ?? deriveWasmExecUrl(opts.wasmUrl);
  await ensureWasmExec(wasmExecUrl);
  const Go = globalThis.Go!;
  const go = new Go();
  if (opts.argv) go.argv = opts.argv;
  if (opts.env) go.env = opts.env;

  const wasmResp = await fetch(opts.wasmUrl.toString());
  if (!wasmResp.ok) {
    throw new Error(`failed to fetch wasm: ${wasmResp.status} ${wasmResp.statusText}`);
  }
  const { instance } = await WebAssembly.instantiateStreaming(wasmResp, go.importObject);
  // Run in the background; the program runs select{} forever.
  void go.run(instance).catch((err) => {
    // eslint-disable-next-line no-console
    console.error('torrent wasm exited:', err);
  });
  await readySignal;
}

function deriveWasmExecUrl(wasmUrl: string | URL): URL {
  const url = new URL(wasmUrl.toString(), typeof window !== 'undefined' ? window.location.href : 'http://localhost');
  url.pathname = url.pathname.replace(/torrent\.wasm$/, 'wasm_exec.js');
  return url;
}
