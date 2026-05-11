package torrent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2"
)

type Listener interface {
	// Accept waits for and returns the next connection to the listener.
	Accept() (net.Conn, error)

	// Addr returns the listener's network address.
	Addr() net.Addr
}

type socket interface {
	Listener
	Dialer
	Close() error
}

func listen(n network, addr string, f firewallCallback, logger *slog.Logger) (socket, error) {
	switch {
	case n.Tcp:
		return listenTcp(n.String(), addr)
	case n.Udp:
		return listenUtp(n.String(), addr, f, logger)
	default:
		panic(n)
	}
}

// Dialing TCP from a local port limits us to a single outgoing TCP connection to each remote
// client. Instead, this should be a last resort if we need to use holepunching, and only then to
// connect to other clients that actually try to holepunch TCP.
const dialTcpFromListenPort = false

var SocketIPTypeOfService = 0

type tcpSocket struct {
	net.Listener
	NetworkDialer
}

type listenFunc func(n network, addr string, f firewallCallback, logger *slog.Logger) (socket, error)

const listenAllRetryLimit = 150

func listenAll(
	networks []network,
	getHost func(string) string,
	port int,
	f firewallCallback,
	logger *slog.Logger,
) ([]socket, error) {
	return listenAllWithListenFunc(networks, getHost, port, f, logger, listen)
}

func listenAllWithListenFunc(
	networks []network,
	getHost func(string) string,
	port int,
	f firewallCallback,
	logger *slog.Logger,
	lf listenFunc,
) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n.String())})
	}
	retries := 0
	for {
		ss, retry, err := listenAllRetry(nahs, port, f, logger, lf)
		if !retry {
			return ss, err
		}
		retries++
		if retries >= listenAllRetryLimit {
			return nil, err
		}
	}
}

type networkAndHost struct {
	Network network
	Host    string
}

func isUnsupportedNetworkError(err error) bool {
	var sysErr *os.SyscallError
	//spewCfg := spew.NewDefaultConfig()
	//spewCfg.ContinueOnMethod = true
	//spewCfg.Dump(err)
	if !errors.As(err, &sysErr) {
		return false
	}
	//spewCfg.Dump(sysErr)
	//spewCfg.Dump(sysErr.Err.Error())
	// This might only be Linux specific.
	return sysErr.Syscall == "bind" && sysErr.Err.Error() == "cannot assign requested address"
}

func listenAllRetry(
	nahs []networkAndHost,
	port int,
	f firewallCallback,
	logger *slog.Logger,
	lf listenFunc,
) (ss []socket, retry bool, err error) {
	// Close all sockets on error or retry.
	defer func() {
		if err != nil || retry {
			for _, s := range ss {
				s.Close()
			}
			ss = nil
		}
	}()
	g.MakeSliceWithCap(&ss, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	for _, nah := range nahs {
		var s socket
		s, err = lf(nah.Network, net.JoinHostPort(nah.Host, portStr), f, logger)
		if err != nil {
			if isUnsupportedNetworkError(err) {
				err = nil
				continue
			}
			if len(ss) == 0 {
				// First relative to a possibly dynamic port (0).
				err = fmt.Errorf("first listen: %w", err)
			} else {
				err = fmt.Errorf("subsequent listen: %w", err)
			}
			retry = port == 0 && len(ss) > 0
			return
		}
		ss = append(ss, s)
		portStr = strconv.FormatInt(int64(missinggo.AddrPort(ss[0].Addr())), 10)
	}
	return
}

// This isn't aliased from go-libutp since that assumes CGO.
type firewallCallback func(net.Addr) bool

func listenUtp(network, addr string, fc firewallCallback, logger *slog.Logger) (socket, error) {
	us, err := NewUtpSocketSlogger(network, addr, fc, logger)
	return utpSocketSocket{us, network}, err
}

// utpSocket wrapper, additionally wrapped for the torrent package's socket interface.
type utpSocketSocket struct {
	utpSocket
	network string
}

func (me utpSocketSocket) DialerNetwork() string {
	return me.network
}

func (me utpSocketSocket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	return me.utpSocket.DialContext(ctx, me.network, addr)
}
