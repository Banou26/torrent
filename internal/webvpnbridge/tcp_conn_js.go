//go:build js && wasm

package webvpnbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall/js"
	"time"
)

// tcpConn implements net.Conn over a JS TcpConn (dataReadableStream +
// dataWritableStream).
type tcpConn struct {
	js     js.Value // The TcpConn JS object.
	reader js.Value // ReadableStreamDefaultReader
	writer js.Value // WritableStreamDefaultWriter

	localAddr  net.Addr
	remoteAddr net.Addr

	// readBuf holds bytes that arrived in a single JS Uint8Array but weren't
	// fully consumed by the caller's Read.
	readMu  sync.Mutex
	readBuf []byte
	readErr error

	closeOnce sync.Once
	closed    chan struct{}
}

func newTcpConnFromJS(v js.Value) *tcpConn {
	c := &tcpConn{
		js:     v,
		reader: v.Get("dataReadableStream").Call("getReader"),
		writer: v.Get("dataWritableStream").Call("getWriter"),
		closed: make(chan struct{}),
	}
	c.localAddr = parseTCPAddr(v.Get("localAddress").String(), v.Get("localPort").Int())
	c.remoteAddr = parseTCPAddr(v.Get("remoteAddress").String(), v.Get("remotePort").Int())
	return c
}

func parseTCPAddr(addr string, port int) net.Addr {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		// Fall back to a string-only representation; consumers shouldn't choke
		// on it because (*net.TCPAddr).String only renders what's set.
		return &net.TCPAddr{Port: port}
	}
	return &net.TCPAddr{IP: ip.AsSlice(), Port: port, Zone: ip.Zone()}
}

func (c *tcpConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	if len(c.readBuf) == 0 {
		if c.readErr != nil {
			err := c.readErr
			c.readMu.Unlock()
			return 0, err
		}
		c.readMu.Unlock()
		// Pull the next chunk without holding the lock.
		buf, err := c.readNextChunk()
		c.readMu.Lock()
		if err != nil {
			c.readErr = err
			c.readMu.Unlock()
			return 0, err
		}
		c.readBuf = buf
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	c.readMu.Unlock()
	return n, nil
}

func (c *tcpConn) readNextChunk() ([]byte, error) {
	select {
	case <-c.closed:
		return nil, io.EOF
	default:
	}
	result, err := await(c.reader.Call("read"))
	if err != nil {
		return nil, fmt.Errorf("reader.read: %w", err)
	}
	if result.Get("done").Bool() {
		return nil, io.EOF
	}
	return readJsBuffer(result.Get("value")), nil
}

func (c *tcpConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	select {
	case <-c.closed:
		return 0, errClosed
	default:
	}
	chunk := newJsBuffer(p)
	if _, err := await(c.writer.Call("write", chunk)); err != nil {
		return 0, fmt.Errorf("writer.write: %w", err)
	}
	return len(p), nil
}

func (c *tcpConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		// Cancel the reader so a pending read returns done. Ignore promise.
		c.reader.Call("cancel")
		// Best-effort writer close.
		c.writer.Call("close")
		// Tell the host to tear down the underlying socket.
		if closeFn := c.js.Get("close"); closeFn.Type() == js.TypeFunction {
			c.js.Call("close")
		} else if destroy := c.js.Get("destroy"); destroy.Type() == js.TypeFunction {
			c.js.Call("destroy")
		}
	})
	return nil
}

func (c *tcpConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *tcpConn) RemoteAddr() net.Addr { return c.remoteAddr }

// Deadlines: not yet implemented. Bittorrent transports rely on
// application-level keepalives, but we still need these to satisfy net.Conn.
func (c *tcpConn) SetDeadline(t time.Time) error      { return nil }
func (c *tcpConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *tcpConn) SetWriteDeadline(t time.Time) error { return nil }

// DialTCP dials a TCP peer via the JS host.
func DialTCP(ctx context.Context, network, address string) (net.Conn, error) {
	host := host()
	host_address, host_port, err := splitHostPort(address)
	if err != nil {
		return nil, err
	}
	opts := js.Global().Get("Object").New()
	opts.Set("network", network)
	opts.Set("address", host_address)
	opts.Set("port", host_port)

	// Allow the ctx to cancel by racing against host's dial.
	type result struct {
		v   js.Value
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		v, err := await(host.Call("dialTcp", opts))
		resCh <- result{v: v, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		return newTcpConnFromJS(r.v), nil
	}
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("bad port %q: %w", portStr, err)
	}
	return host, port, nil
}

// Sanity check at compile time.
var _ net.Conn = (*tcpConn)(nil)
var _ = errors.New // keep import
