// Package jsbridge provides Go-side adapters that delegate networking and
// storage to a JavaScript bridge installed at globalThis.__torrentBridge.
//
// It is intended for the GOOS=js GOARCH=wasm build only. Files in this
// package use //go:build js && wasm so the package does not interfere with
// the normal build of the upstream torrent library.
//
// The companion TypeScript code in web/ts wires the bridge to the
// browser's native APIs (OPFS, Direct Sockets, etc.) and to @fkn/lib for
// nodejs-compatible net/dgram polyfills.

//go:build js && wasm

package jsbridge

import (
	"errors"
	"fmt"
	"syscall/js"
)

// bridge returns the JS object that the host has installed at
// globalThis.__torrentBridge. It panics if the bridge has not been
// installed: this is a hard programming error from the host.
func bridge() js.Value {
	v := js.Global().Get("__torrentBridge")
	if v.IsUndefined() || v.IsNull() {
		panic("globalThis.__torrentBridge is not installed; the JS host must install it before running the WASM module")
	}
	return v
}

// awaitPromise blocks the current goroutine until the given JS Promise
// settles. It returns the resolved value or an error wrapping the
// rejection reason.
func awaitPromise(p js.Value) (js.Value, error) {
	if p.Type() != js.TypeObject || p.Get("then").Type() != js.TypeFunction {
		// Not a thenable: treat as a synchronous value.
		return p, nil
	}
	type result struct {
		val js.Value
		err error
	}
	ch := make(chan result, 1)
	var onFulfilled, onRejected js.Func
	cleanup := func() {
		onFulfilled.Release()
		onRejected.Release()
	}
	onFulfilled = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{val: v}
		return nil
	})
	onRejected = js.FuncOf(func(this js.Value, args []js.Value) any {
		var msg string
		if len(args) > 0 {
			arg := args[0]
			if arg.Type() == js.TypeObject && !arg.Get("message").IsUndefined() {
				msg = arg.Get("message").String()
			} else {
				msg = arg.String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	p.Call("then", onFulfilled, onRejected)
	r := <-ch
	cleanup()
	return r.val, r.err
}

// callPromise invokes method on bridge with args and awaits its returned
// Promise. The bridge methods are expected to be async.
func callPromise(method string, args ...any) (js.Value, error) {
	defer func() {
		if r := recover(); r != nil {
			// Convert syscall/js panics (e.g. ValueOf-of-invalid type) into
			// proper errors so they don't bring down the Go runtime.
			err := fmt.Errorf("jsbridge: panic calling %q: %v", method, r)
			_ = err // surfaced via the named return below
		}
	}()
	p := bridge().Call(method, args...)
	return awaitPromise(p)
}

// uint8Array constructs a Uint8Array view backed by a new JS buffer of the
// given length. The caller can write into it with js.CopyBytesToJS.
func uint8Array(n int) js.Value {
	return js.Global().Get("Uint8Array").New(n)
}

// jsToBytes copies a Uint8Array js.Value into a Go []byte.
func jsToBytes(v js.Value) []byte {
	if v.IsUndefined() || v.IsNull() {
		return nil
	}
	n := v.Get("byteLength").Int()
	b := make([]byte, n)
	js.CopyBytesToGo(b, v)
	return b
}

// bytesToJS copies a Go []byte into a freshly-allocated Uint8Array.
func bytesToJS(b []byte) js.Value {
	arr := uint8Array(len(b))
	if len(b) > 0 {
		js.CopyBytesToJS(arr, b)
	}
	return arr
}
