//go:build js && wasm

package jsbridge

import (
	"context"
	"log/slog"
	"net"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/dialer"
	"github.com/anacrolix/utp"
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
		// Drive real uTP on top of the JS-backed UDP socket. anacrolix/utp's
		// Socket implements Accept/Dial/Addr/Close — we only need to wrap it
		// to satisfy torrent.Socket (which expects DialerNetwork() and a
		// context-aware Dial signature).
		us, err := utp.NewSocketFromPacketConn(pc)
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		return &utpSocket{us: us, network: network}, nil
	}
}

// tcpSocket adapts a JsListener + outbound JsConn dialer to torrent.Socket.
type tcpSocket struct {
	*JsListener
	network string
}

func (t *tcpSocket) DialerNetwork() string { return t.network }

func (t *tcpSocket) Dial(ctx context.Context, addr string) (net.Conn, error) {
	// Avoid the Go typed-nil interface trap: never return a non-nil
	// net.Conn interface value wrapping a nil *JsConn pointer.
	c, err := DialJsConn(ctx, t.network, addr)
	if err != nil {
		return nil, err
	}
	return c, nil
}

var _ dialer.T = (*tcpSocket)(nil)

// utpSocket wraps an anacrolix/utp.Socket to satisfy torrent.Socket.
// The underlying transport is a JsPacketConn (UDP from @fkn/lib's
// dgram polyfill). All uTP framing happens locally in Go.
type utpSocket struct {
	us      *utp.Socket
	network string
}

func (u *utpSocket) Accept() (net.Conn, error) { return u.us.Accept() }
func (u *utpSocket) Addr() net.Addr            { return u.us.Addr() }
func (u *utpSocket) Close() error              { return u.us.Close() }
func (u *utpSocket) DialerNetwork() string     { return u.network }

func (u *utpSocket) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return u.us.DialContext(ctx, u.network, addr)
}

var _ dialer.T = (*utpSocket)(nil)
