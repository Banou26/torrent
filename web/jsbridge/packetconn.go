//go:build js && wasm

package jsbridge

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"syscall/js"
	"time"
)

// JsPacketConn is a net.PacketConn backed by the JS bridge's UDP
// implementation (typically @fkn/lib dgram). The bridge identifies
// sockets by a numeric ID.
type JsPacketConn struct {
	id      int
	network string
	local   net.Addr

	closeOnce sync.Once
	closed    chan struct{}
}

var _ net.PacketConn = (*JsPacketConn)(nil)

// ListenPacketJS opens a UDP socket through the host. The host returns
// { id, localAddr } where localAddr is "host:port".
//
// If the host rejects with an EADDRINUSE / EADDRNOTAVAIL-style failure
// (typically on the second IP family for a port that the first family
// already owns dual-stack), we surface an *os.SyscallError that matches
// the upstream library's isUnsupportedNetworkError pattern so it skips
// the family and continues.
func ListenPacketJS(network, address string) (*JsPacketConn, error) {
	v, err := callPromise("packetListen", network, address)
	if err != nil {
		if looksLikeUnsupportedAddr(err.Error()) {
			return nil, &os.SyscallError{
				Syscall: "bind",
				Err:     errors.New("cannot assign requested address"),
			}
		}
		return nil, err
	}
	id := v.Get("id").Int()
	local := v.Get("localAddr").String()
	return &JsPacketConn{
		id:      id,
		network: network,
		local:   stringAddr{network: network, address: local},
		closed:  make(chan struct{}),
	}, nil
}

func looksLikeUnsupportedAddr(msg string) bool {
	for _, marker := range []string{"EADDRINUSE", "EADDRNOTAVAIL", "EAFNOSUPPORT"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ReadFrom blocks waiting for the next inbound datagram. The host
// returns { data: Uint8Array, addr: "host:port" } or null on closure.
func (p *JsPacketConn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	v, err := callPromise("packetReadFrom", p.id)
	if err != nil {
		return 0, nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return 0, nil, net.ErrClosed
	}
	data := v.Get("data")
	dn := data.Get("byteLength").Int()
	if dn > len(b) {
		dn = len(b)
	}
	js.CopyBytesToGo(b[:dn], data)
	addrStr := v.Get("addr").String()
	return dn, stringAddr{network: p.network, address: addrStr}, nil
}

// WriteTo sends a datagram via the host. addr must stringify to
// "host:port". The host returns { n: number } on success.
func (p *JsPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	v, err := callPromise("packetWriteTo", p.id, bytesToJS(b), addr.String())
	if err != nil {
		return 0, err
	}
	return v.Get("n").Int(), nil
}

func (p *JsPacketConn) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		_, _ = callPromise("packetClose", p.id)
	})
	return nil
}

func (p *JsPacketConn) LocalAddr() net.Addr { return p.local }

func (p *JsPacketConn) SetDeadline(t time.Time) error {
	_, _ = callPromise("packetSetDeadline", p.id, t.UnixMilli())
	return nil
}

func (p *JsPacketConn) SetReadDeadline(t time.Time) error {
	_, _ = callPromise("packetSetReadDeadline", p.id, t.UnixMilli())
	return nil
}

func (p *JsPacketConn) SetWriteDeadline(t time.Time) error {
	_, _ = callPromise("packetSetWriteDeadline", p.id, t.UnixMilli())
	return nil
}
