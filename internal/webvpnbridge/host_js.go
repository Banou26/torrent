//go:build js && wasm

package webvpnbridge

import (
	"errors"
	"fmt"
	"syscall/js"
)

const hostGlobal = "__torrent_host"

var (
	uint8Array = js.Global().Get("Uint8Array")
	errClosed  = errors.New("webvpnbridge: closed")
)

// Host returns the JS host object that fulfils the bridge contract. It panics
// if the host hasn't been installed — the JS wrapper is expected to set
// globalThis.__torrent_host before invoking Go.
func host() js.Value {
	h := js.Global().Get(hostGlobal)
	if h.IsUndefined() || h.IsNull() {
		panic("webvpnbridge: globalThis." + hostGlobal + " is not installed")
	}
	return h
}

// await blocks the calling goroutine until the given thenable settles. The
// returned value mirrors the JS Promise resolution; on rejection a Go error
// is returned. js.Func handles are released before await returns to avoid
// accumulating function references on the JS side.
func await(promise js.Value) (js.Value, error) {
	type result struct {
		value js.Value
		err   error
	}
	ch := make(chan result, 1)
	var resolve, reject js.Func
	resolve = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{value: v}
		return nil
	})
	reject = js.FuncOf(func(this js.Value, args []js.Value) any {
		var msg string
		if len(args) > 0 {
			msg = jsErrorMessage(args[0])
		} else {
			msg = "promise rejected"
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	promise.Call("then", resolve, reject)
	r := <-ch
	resolve.Release()
	reject.Release()
	return r.value, r.err
}

func jsErrorMessage(v js.Value) string {
	if v.Type() == js.TypeObject {
		if msg := v.Get("message"); msg.Type() == js.TypeString {
			return msg.String()
		}
		if s := v.Call("toString"); s.Type() == js.TypeString {
			return s.String()
		}
	}
	if v.Type() == js.TypeString {
		return v.String()
	}
	return fmt.Sprintf("non-error rejection: %v", v)
}

// newJsBuffer copies a Go byte slice into a freshly-allocated JS Uint8Array.
func newJsBuffer(b []byte) js.Value {
	buf := uint8Array.New(len(b))
	js.CopyBytesToJS(buf, b)
	return buf
}

// readJsBuffer extracts a Uint8Array-like value into a Go byte slice.
func readJsBuffer(v js.Value) []byte {
	// If the value is an ArrayBuffer, wrap it in a Uint8Array.
	if v.InstanceOf(js.Global().Get("ArrayBuffer")) {
		v = uint8Array.New(v)
	}
	length := v.Get("byteLength").Int()
	if length == 0 {
		return nil
	}
	out := make([]byte, length)
	js.CopyBytesToGo(out, v)
	return out
}
