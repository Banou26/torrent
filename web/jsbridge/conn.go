//go:build js && wasm

package jsbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall/js"
	"time"
)

// JsConn is a net.Conn that delegates all I/O to the JS bridge.
//
// The bridge identifies sockets by an opaque numeric ID. Reads, writes,
// and close all forward to the host via async methods.
type JsConn struct {
	id         int
	network    string
	localAddr  net.Addr
	remoteAddr net.Addr

	readMu  sync.Mutex
	writeMu sync.Mutex
	closed  bool
	closeMu sync.Mutex

	readDeadline  time.Time
	writeDeadline time.Time
}

var _ net.Conn = (*JsConn)(nil)

// DialJsConn dials network/addr through the JS bridge and returns a Conn.
// ctx is observed for cancellation.
func DialJsConn(ctx context.Context, network, addr string) (*JsConn, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := callPromise("dial", network, addr)
		ch <- result{v, err}
	}()
	select {
	case <-ctx.Done():
		// We don't have a cancel channel back to JS yet; best-effort.
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return newJsConnFromJS(network, r.v), nil
	}
}

// newJsConnFromJS constructs a JsConn from a JS-returned object of shape
// { id: number, localAddr: string, remoteAddr: string }.
func newJsConnFromJS(network string, v js.Value) *JsConn {
	id := v.Get("id").Int()
	local := v.Get("localAddr").String()
	remote := v.Get("remoteAddr").String()
	return &JsConn{
		id:         id,
		network:    network,
		localAddr:  stringAddr{network: network, address: local},
		remoteAddr: stringAddr{network: network, address: remote},
	}
}

func (c *JsConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	v, err := callPromise("connRead", c.id, len(b))
	if err != nil {
		return 0, err
	}
	if v.IsNull() || v.IsUndefined() {
		return 0, io.EOF
	}
	data := v.Get("data")
	n := data.Get("byteLength").Int()
	if n == 0 {
		// The JS side may return an empty buffer to signal EOF.
		if v.Get("eof").Truthy() {
			return 0, io.EOF
		}
		return 0, nil
	}
	if n > len(b) {
		n = len(b)
	}
	js.CopyBytesToGo(b[:n], data)
	return n, nil
}

func (c *JsConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	v, err := callPromise("connWrite", c.id, bytesToJS(b))
	if err != nil {
		return 0, err
	}
	n := v.Get("n").Int()
	return n, nil
}

func (c *JsConn) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.closeMu.Unlock()
	_, err := callPromise("connClose", c.id)
	return err
}

func (c *JsConn) isClosed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closed
}

func (c *JsConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *JsConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *JsConn) SetDeadline(t time.Time) error {
	c.readDeadline = t
	c.writeDeadline = t
	// Forward as a best-effort hint. The bridge may ignore it.
	_, _ = callPromise("connSetDeadline", c.id, t.UnixMilli())
	return nil
}

func (c *JsConn) SetReadDeadline(t time.Time) error {
	c.readDeadline = t
	_, _ = callPromise("connSetReadDeadline", c.id, t.UnixMilli())
	return nil
}

func (c *JsConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline = t
	_, _ = callPromise("connSetWriteDeadline", c.id, t.UnixMilli())
	return nil
}

// ErrNotImplemented is returned when the JS bridge does not provide a
// feature requested by the Go runtime.
var ErrNotImplemented = errors.New("jsbridge: not implemented by host")
