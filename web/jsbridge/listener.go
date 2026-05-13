//go:build js && wasm

package jsbridge

import (
	"errors"
	"net"
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
func listenTcpBridge(network, address string) (*JsListener, error) {
	v, err := callPromise("listenTcp", network, address)
	if err != nil {
		return nil, err
	}
	if v.IsUndefined() || v.IsNull() {
		return nil, errors.New("jsbridge: host returned no listener (incoming TCP unsupported?)")
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
