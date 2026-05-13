//go:build js && wasm

package main

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/web/jsbridge"
)

// Default public DNS servers used by the wasm build. We prefer DoT/DoH
// in the long run, but plain DNS-over-UDP to Cloudflare/Google works
// fine through any host that polyfills `dgram`.
var defaultResolvers = []string{
	"1.1.1.1:53",
	"1.0.0.1:53",
	"8.8.8.8:53",
}

// installResolver replaces net.DefaultResolver.Dial so that Go's DNS
// lookups go through our JS-backed UDP socket instead of the (stub) net
// stack the js/wasm runtime ships with.
//
// We dial a virtual "connected" PacketConn that wraps a JsPacketConn and
// pins the remote address to a public resolver. The standard library's
// resolver issues a UDP query and reads back a single response — that
// fits the connectedPacketConn semantics perfectly.
func installResolver() {
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		// Ignore `address` (which Go fills from /etc/resolv.conf and on
		// js/wasm is whatever the runtime guesses — typically `[::1]:53`).
		// Use our hard-coded public resolvers instead.
		isUDP := strings.HasPrefix(network, "udp")
		fam := "udp4"
		if !isUDP {
			// We don't currently implement TCP DNS through the bridge;
			// Go falls back to UDP automatically. Reject so it does.
			return nil, &net.OpError{Op: "dial", Net: network, Err: errDNSTCPUnsupported}
		}
		_ = fam
		// Pick the first resolver and bind a UDP socket via the bridge.
		pc, err := jsbridge.ListenPacketJS("udp4", "0.0.0.0:0")
		if err != nil {
			return nil, err
		}
		raddr := pickResolver()
		return newConnectedPacketConn(pc, raddr), nil
	}
}

var (
	resolverIndex   = 0
	resolverIndexMu sync.Mutex
)

func pickResolver() net.Addr {
	resolverIndexMu.Lock()
	defer resolverIndexMu.Unlock()
	s := defaultResolvers[resolverIndex%len(defaultResolvers)]
	resolverIndex++
	host, port, _ := net.SplitHostPort(s)
	return &net.UDPAddr{IP: net.ParseIP(host), Port: atoiSafe(port)}
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// errDNSTCPUnsupported is returned when the resolver asks for a
// TCP-based lookup; we let it fall back to UDP.
var errDNSTCPUnsupported = stringErr("jsbridge resolver: TCP DNS not supported, fall back to UDP")

type stringErr string

func (s stringErr) Error() string { return string(s) }

// connectedPacketConn wraps a net.PacketConn with a pinned remote
// address. It satisfies *both* net.Conn AND net.PacketConn — Go's DNS
// resolver type-asserts on net.PacketConn to choose UDP semantics
// (raw datagram exchange, no 2-byte length prefix), so we must expose
// both surfaces.
type connectedPacketConn struct {
	pc     net.PacketConn
	remote net.Addr

	rdMu sync.Mutex
}

func newConnectedPacketConn(pc net.PacketConn, remote net.Addr) net.Conn {
	return &connectedPacketConn{pc: pc, remote: remote}
}

// net.Conn surface.

func (c *connectedPacketConn) Read(b []byte) (int, error) {
	c.rdMu.Lock()
	defer c.rdMu.Unlock()
	for {
		n, from, err := c.pc.ReadFrom(b)
		if err != nil {
			return n, err
		}
		if from.String() == c.remote.String() {
			return n, nil
		}
		// Different source: ignore and keep reading.
	}
}

func (c *connectedPacketConn) Write(b []byte) (int, error) {
	return c.pc.WriteTo(b, c.remote)
}

// net.PacketConn surface. Read/WriteTo delegate to the underlying conn
// without source-address filtering.

func (c *connectedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	return c.pc.ReadFrom(b)
}

func (c *connectedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return c.pc.WriteTo(b, addr)
}

// Common.

func (c *connectedPacketConn) Close() error                       { return c.pc.Close() }
func (c *connectedPacketConn) LocalAddr() net.Addr                { return c.pc.LocalAddr() }
func (c *connectedPacketConn) RemoteAddr() net.Addr               { return c.remote }
func (c *connectedPacketConn) SetDeadline(t time.Time) error      { return c.pc.SetDeadline(t) }
func (c *connectedPacketConn) SetReadDeadline(t time.Time) error  { return c.pc.SetReadDeadline(t) }
func (c *connectedPacketConn) SetWriteDeadline(t time.Time) error { return c.pc.SetWriteDeadline(t) }

// Compile-time assertions: connectedPacketConn must satisfy both
// interfaces so that Go's resolver picks the UDP roundtrip path.
var (
	_ net.Conn       = (*connectedPacketConn)(nil)
	_ net.PacketConn = (*connectedPacketConn)(nil)
)
