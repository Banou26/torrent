//go:build !(js && wasm)

package torrent

import (
	"context"
	"log/slog"
	"net"
	"syscall"
)

var tcpListenConfig = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) (err error) {
		controlErr := c.Control(func(fd uintptr) {
			if dialTcpFromListenPort {
				err = setReusePortSockOpts(fd)
			}
			if err == nil && SocketIPTypeOfService != 0 {
				err = setSockIPTOS(fd, SocketIPTypeOfService)
			}
		})
		if err != nil {
			return
		}
		err = controlErr
		return
	},
	// BitTorrent connections manage their own keep-alives.
	KeepAlive: -1,
}

func listenTcp(network, address string) (s socket, err error) {
	l, err := tcpListenConfig.Listen(context.Background(), network, address)
	if err != nil {
		return
	}
	netDialer := net.Dialer{
		FallbackDelay: -1,
		KeepAlive:     tcpListenConfig.KeepAlive,
		Control: func(network, address string, c syscall.RawConn) (err error) {
			controlErr := c.Control(func(fd uintptr) {
				err = setSockNoLinger(fd)
				if err != nil {
					slog.Debug("error setting linger socket option on tcp socket", "err", err)
					err = nil
				}
				if dialTcpFromListenPort {
					err = setReusePortSockOpts(fd)
				}
				if err == nil && SocketIPTypeOfService != 0 {
					err = setSockIPTOS(fd, SocketIPTypeOfService)
				}
			})
			if err == nil {
				err = controlErr
			}
			return
		},
	}
	if dialTcpFromListenPort {
		netDialer.LocalAddr = l.Addr()
	}
	s = tcpSocket{
		Listener: l,
		NetworkDialer: NetworkDialer{
			Network: network,
			Dialer:  &netDialer,
		},
	}
	return
}
