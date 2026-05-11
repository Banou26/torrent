//go:build js && wasm

package torrent

import (
	"log/slog"

	"github.com/anacrolix/log"
	"github.com/anacrolix/utp"

	"github.com/anacrolix/torrent/internal/webvpnbridge"
)

// NewUtpSocketSlogger on wasm wires the pure-Go uTP socket onto a
// browser-side UDP packet connection supplied by the JS host. The firewall
// callback is ignored because hostile inbound connections can't realistically
// be filtered before they're tunnelled to us.
func NewUtpSocketSlogger(network, addr string, _ firewallCallback, _ *slog.Logger) (utpSocket, error) {
	pc, err := webvpnbridge.BindUDP(network, addr)
	if err != nil {
		return nil, err
	}
	s, err := utp.NewSocketFromPacketConn(pc)
	if err != nil {
		pc.Close()
		return nil, err
	}
	return s, nil
}

// Deprecated: Use [NewUtpSocketSlogger].
func NewUtpSocket(network, addr string, fc firewallCallback, logger log.Logger) (utpSocket, error) {
	var sl *slog.Logger
	if !logger.IsZero() {
		sl = logger.Slogger()
	} else {
		sl = slog.Default()
	}
	return NewUtpSocketSlogger(network, addr, fc, sl)
}
