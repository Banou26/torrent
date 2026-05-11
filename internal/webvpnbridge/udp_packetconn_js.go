//go:build js && wasm

package webvpnbridge

import (
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

// udpPacketConn implements net.PacketConn over a JS UdpSocket.
type udpPacketConn struct {
	js     js.Value
	reader js.Value // ReadableStreamDefaultReader over the datagrams stream.

	localAddr *net.UDPAddr

	readMu      sync.Mutex
	pendingData []byte
	pendingAddr *net.UDPAddr
	readErr     error

	closeOnce sync.Once
	closed    chan struct{}
}

func newUdpPacketConnFromJS(v js.Value) *udpPacketConn {
	return &udpPacketConn{
		js:        v,
		reader:    v.Get("dataReadableStream").Call("getReader"),
		localAddr: parseUDPAddr(v.Get("localAddress").String(), v.Get("localPort").Int()),
		closed:    make(chan struct{}),
	}
}

func parseUDPAddr(addr string, port int) *net.UDPAddr {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return &net.UDPAddr{Port: port}
	}
	return &net.UDPAddr{IP: ip.AsSlice(), Port: port, Zone: ip.Zone()}
}

func (c *udpPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	if len(c.pendingData) > 0 {
		n := copy(p, c.pendingData)
		addr := c.pendingAddr
		c.pendingData = c.pendingData[n:]
		// UDP semantics: each datagram should be returned in a single ReadFrom
		// call. If the caller supplied a buffer smaller than the datagram, the
		// remainder is discarded.
		if len(c.pendingData) > 0 {
			c.pendingData = nil
		}
		c.readMu.Unlock()
		return n, addr, nil
	}
	if c.readErr != nil {
		err := c.readErr
		c.readMu.Unlock()
		return 0, nil, err
	}
	c.readMu.Unlock()
	select {
	case <-c.closed:
		return 0, nil, errClosed
	default:
	}
	res, err := await(c.reader.Call("read"))
	if err != nil {
		c.readMu.Lock()
		c.readErr = err
		c.readMu.Unlock()
		return 0, nil, fmt.Errorf("udp reader.read: %w", err)
	}
	if res.Get("done").Bool() {
		c.readMu.Lock()
		c.readErr = io.EOF
		c.readMu.Unlock()
		return 0, nil, io.EOF
	}
	value := res.Get("value")
	data := readJsBuffer(value.Get("data"))
	addrStr := value.Get("address").String()
	port := value.Get("port").Int()
	addr := parseUDPAddr(addrStr, port)
	n := copy(p, data)
	if n < len(data) {
		// Excess is dropped, mirroring real UDP socket behaviour.
	}
	return n, addr, nil
}

func (c *udpPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, errClosed
	default:
	}
	host, port, err := udpAddrParts(addr)
	if err != nil {
		return 0, err
	}
	opts := js.Global().Get("Object").New()
	// Send a freshly-allocated ArrayBuffer-backed Uint8Array to detach Go memory.
	chunk := newJsBuffer(p)
	opts.Set("message", chunk.Get("buffer"))
	opts.Set("address", host)
	opts.Set("port", port)
	if _, err := await(c.js.Call("send", opts)); err != nil {
		return 0, fmt.Errorf("udp send: %w", err)
	}
	return len(p), nil
}

func udpAddrParts(addr net.Addr) (string, int, error) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		host := a.IP.String()
		if host == "<nil>" {
			host = ""
		}
		return host, a.Port, nil
	}
	host, portStr, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func (c *udpPacketConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.reader.Call("cancel")
		if closeFn := c.js.Get("close"); closeFn.Type() == js.TypeFunction {
			c.js.Call("close")
		}
	})
	return nil
}

func (c *udpPacketConn) LocalAddr() net.Addr { return c.localAddr }

func (c *udpPacketConn) SetDeadline(t time.Time) error      { return nil }
func (c *udpPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *udpPacketConn) SetWriteDeadline(t time.Time) error { return nil }

// BindUDP binds a UDP packet conn through the JS host.
func BindUDP(network, address string) (net.PacketConn, error) {
	h := host()
	hostAddr, port, err := splitHostPort(address)
	if err != nil {
		return nil, err
	}
	opts := js.Global().Get("Object").New()
	opts.Set("network", network)
	opts.Set("address", hostAddr)
	opts.Set("port", port)
	res, err := await(h.Call("bindUdp", opts))
	if err != nil {
		return nil, err
	}
	return newUdpPacketConnFromJS(res), nil
}

var _ net.PacketConn = (*udpPacketConn)(nil)
var _ = errors.New
