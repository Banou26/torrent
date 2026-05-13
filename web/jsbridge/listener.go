//go:build js && wasm

package jsbridge

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall/js"
)

// JsListener accepts inbound TCP connections via the JS bridge.
//
// Browsers cannot accept arbitrary inbound TCP connections; in practice
// the host will either: (a) refuse to open a listener and the library
// will fall back to outbound-only behaviour, or (b) proxy inbound
// connections through some other mechanism (a relay, Chrome's
// experimental Direct Sockets accept(), etc.).
type JsListener struct {
	id      int
	network string
	addr    net.Addr

	closeOnce sync.Once
	closed    chan struct{}
}

var _ net.Listener = (*JsListener)(nil)

// listenTcpBridge opens a JS-backed TCP listener. The returned object has
// shape { id, localAddr }.
//
// When the host returns null (refusing or failing to bind), we surface an
// *os.SyscallError that matches the upstream library's isUnsupportedNetworkError
// pattern, so the library skips this network family and continues with the
// others. This is how we degrade gracefully on, e.g., a tcp6 EADDRINUSE that
// the host's TCP server can't recover from.
func listenTcpBridge(network, address string) (*JsListener, error) {
	v, err := callPromise("listenTcp", network, address)
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, &os.SyscallError{
			Syscall: "bind",
			Err:     errors.New("cannot assign requested address"),
		}
	}
	id := v.Get("id").Int()
	local := v.Get("localAddr").String()
	return &JsListener{
		id:      id,
		network: network,
		addr:    stringAddr{network: network, address: local},
		closed:  make(chan struct{}),
	}, nil
}

// Accept waits for the host to deliver an inbound connection.
func (l *JsListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	v, err := callPromise("listenerAccept", l.id)
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, net.ErrClosed
	}
	return newJsConnFromJS(l.network, v), nil
}

func (l *JsListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		_, _ = callPromise("listenerClose", l.id)
	})
	return nil
}

func (l *JsListener) Addr() net.Addr { return l.addr }

// Helpers so tests can inspect the listener ID.
func (l *JsListener) ID() int       { return l.id }
func (l *JsListener) JSValue() js.Value {
	return js.ValueOf(l.id)
}
