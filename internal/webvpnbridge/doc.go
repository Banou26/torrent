// Package webvpnbridge bridges the torrent client's networking to a JS host
// running in the browser via syscall/js, returning real net.Conn,
// net.Listener, and net.PacketConn values that wrap WHATWG streams supplied
// by the host.
//
// The package is compiled only for the js/wasm target. Builds for other
// targets receive an empty package via build tags.
//
// JS contract:
//
// The Go runtime expects a host object on globalThis.__torrent_host with this
// shape (TypeScript-ish):
//
//	type TcpConn = {
//	  localAddress: string, localFamily: 'IPv4'|'IPv6', localPort: number,
//	  remoteAddress: string, remoteFamily: 'IPv4'|'IPv6', remotePort: number,
//	  dataReadableStream: ReadableStream<Uint8Array>,
//	  dataWritableStream: WritableStream<Uint8Array>,
//	  close: () => Promise<void>,
//	}
//
//	type TcpListener = {
//	  localAddress: string, localFamily: 'IPv4'|'IPv6', localPort: number,
//	  // Pull-style accept; rejects when the listener closes.
//	  accept: () => Promise<TcpConn>,
//	  close: () => Promise<void>,
//	}
//
//	type UdpDatagram = {
//	  data: ArrayBuffer | Uint8Array,
//	  address: string, port: number, family: 'IPv4'|'IPv6',
//	}
//
//	type UdpSocket = {
//	  localAddress: string, localFamily: 'IPv4'|'IPv6', localPort: number,
//	  dataReadableStream: ReadableStream<UdpDatagram>,
//	  send: (opts: { message: ArrayBuffer, address: string, port: number }) => Promise<void>,
//	  close: () => Promise<void>,
//	}
//
//	type TorrentHost = {
//	  dialTcp: (opts: { network: 'tcp4'|'tcp6', address: string, port: number }) => Promise<TcpConn>,
//	  listenTcp: (opts: { network: 'tcp4'|'tcp6', address: string, port: number }) => Promise<TcpListener>,
//	  bindUdp: (opts: { network: 'udp4'|'udp6', address: string, port: number }) => Promise<UdpSocket>,
//	}
//
// Reads use reader.read() against the supplied ReadableStream; writes use a
// writer obtained from the WritableStream. All Go-visible blocking is done on
// Go channels — JS continues to run on its single thread between awaits, and
// callbacks are released eagerly to avoid leaking js.Func entries.
package webvpnbridge
