//go:build js && wasm

package webvpnbridge

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall/js"
)

// tcpListener implements net.Listener backed by a JS TcpListener.
type tcpListener struct {
	js js.Value

	addr net.Addr

	closeOnce sync.Once
	closed    chan struct{}
}

func newTcpListenerFromJS(v js.Value) *tcpListener {
	return &tcpListener{
		js:     v,
		addr:   parseTCPAddr(v.Get("localAddress").String(), v.Get("localPort").Int()),
		closed: make(chan struct{}),
	}
}

func (l *tcpListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, errClosed
	default:
	}
	res, err := await(l.js.Call("accept"))
	if err != nil {
		select {
		case <-l.closed:
			return nil, errClosed
		default:
			return nil, fmt.Errorf("accept: %w", err)
		}
	}
	if res.IsNull() || res.IsUndefined() {
		return nil, errClosed
	}
	return newTcpConnFromJS(res), nil
}

func (l *tcpListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		if closeFn := l.js.Get("close"); closeFn.Type() == js.TypeFunction {
			l.js.Call("close")
		}
	})
	return nil
}

func (l *tcpListener) Addr() net.Addr { return l.addr }

// ListenTCP starts a TCP listener through the JS host.
func ListenTCP(network, address string) (net.Listener, error) {
	h := host()
	hostAddr, port, err := splitHostPort(address)
	if err != nil {
		return nil, err
	}
	opts := js.Global().Get("Object").New()
	opts.Set("network", network)
	opts.Set("address", hostAddr)
	opts.Set("port", port)
	res, err := await(h.Call("listenTcp", opts))
	if err != nil {
		return nil, err
	}
	return newTcpListenerFromJS(res), nil
}

var _ net.Listener = (*tcpListener)(nil)
var _ = netip.Addr{}
