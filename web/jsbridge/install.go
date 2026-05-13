//go:build js && wasm

package jsbridge

import (
	"context"
	"log/slog"
	"net"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/dialer"
)

// Install wires the jsbridge implementations into the torrent package.
// Call it once at program startup, before constructing a Client.
func Install() {
	torrent.JsBridgeListenTcp = func(network, address string) (torrent.Socket, error) {
		l, err := listenTcpBridge(network, address)
		if err != nil {
			return nil, err
		}
		return &tcpSocket{
			JsListener: l,
			network:    network,
		}, nil
	}
	torrent.JsBridgeListenUtp = func(network, address string, _ *slog.Logger) (torrent.Socket, error) {
		pc, err := ListenPacketJS(network, address)
		if err != nil {
			return nil, err
		}
		return &utpSocket{pc: pc, network: network}, nil
	}
}

// tcpSocket adapts a JsListener + outbound JsConn dialer to torrent.Socket.
type tcpSocket struct {
	*JsListener
	network string
}

func (t *tcpSocket) DialerNetwork() string { return t.network }

func (t *tcpSocket) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return DialJsConn(ctx, t.network, addr)
}

// Make sure tcpSocket satisfies dialer.T (Dial + DialerNetwork).
var _ dialer.T = (*tcpSocket)(nil)

// utpSocket adapts a JsPacketConn to torrent.Socket. Outbound "uTP" dials
// fall through to the bridge's dial method; the host is responsible for
// running uTP semantics (or, more pragmatically, terminating the
// underlying transport for us). For browser environments where real uTP
// over UDP is impractical, the host may proxy these connections.
type utpSocket struct {
	pc      *JsPacketConn
	network string
}

func (u *utpSocket) Accept() (net.Conn, error) {
	// Inbound uTP "Accept" is handled host-side. Block until the host
	// returns a virtual connection, or returns null on closure.
	v, err := callPromise("utpAccept", u.pc.id)
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, net.ErrClosed
	}
	return newJsConnFromJS(u.network, v), nil
}

func (u *utpSocket) Addr() net.Addr        { return u.pc.LocalAddr() }
func (u *utpSocket) DialerNetwork() string { return u.network }

func (u *utpSocket) Dial(ctx context.Context, addr string) (net.Conn, error) {
	v, err := callPromise("utpDial", u.pc.id, addr)
	if err != nil {
		return nil, err
	}
	return newJsConnFromJS(u.network, v), nil
}

func (u *utpSocket) Close() error { return u.pc.Close() }

var _ dialer.T = (*utpSocket)(nil)
