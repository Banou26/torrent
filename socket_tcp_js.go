//go:build js && wasm

package torrent

import (
	"context"
	"net"

	"github.com/anacrolix/torrent/internal/webvpnbridge"
)

// listenTcp on wasm tunnels TCP through the JS host (typically the FKN
// WebVPN). Direct net.Listen/net.Dial are not available in the browser.
func listenTcp(network, address string) (socket, error) {
	l, err := webvpnbridge.ListenTCP(network, address)
	if err != nil {
		return nil, err
	}
	return tcpSocket{
		Listener: l,
		NetworkDialer: NetworkDialer{
			Network: network,
			Dialer:  webvpnDialer{network: network},
		},
	}, nil
}

type webvpnDialer struct {
	network string
}

func (w webvpnDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "" {
		network = w.network
	}
	return webvpnbridge.DialTCP(ctx, network, address)
}
