//go:build js && wasm

package jsbridge

import "net"

// stringAddr is a net.Addr backed by a plain string. The bridge surface
// works in terms of opaque "host:port" strings, so we don't try to model
// IPs natively on the Go side.
type stringAddr struct {
	network string
	address string
}

func (a stringAddr) Network() string { return a.network }
func (a stringAddr) String() string  { return a.address }

var _ net.Addr = stringAddr{}
