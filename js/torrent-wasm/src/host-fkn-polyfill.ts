// Adapter that fulfils the TorrentHost contract using @fkn/lib's published
// Node-shaped polyfills (net.Socket / net.Server / dgram.Socket).
//
// This is the broadest-compatibility path: it works against the @fkn/lib
// version that's on npm today (which doesn't yet export the raw stream APIs
// from src/api/webvpn). If you can expose those, use ./host-fkn instead —
// it's a thinner translation.

import type { Socket as NetSocket, Server as NetServer } from 'net'
import type { Socket as DgramSocket } from 'dgram'

import type {
  IpFamily,
  TcpConn,
  TcpListener,
  TorrentHost,
  UdpDatagram,
  UdpSocket,
} from './types.js'

export type FknPolyfillModule = {
  net: {
    Socket: typeof NetSocket
    Server: typeof NetServer
  }
  dgram: {
    Socket: typeof DgramSocket
    createSocket: (type: 'udp4' | 'udp6') => DgramSocket
  }
}

/**
 * Build a TorrentHost from @fkn/lib's Node-shaped polyfills. The lib argument
 * is expected to be the default export of @fkn/lib, or an object shaped like
 * { net, dgram }. We accept it as a parameter so callers don't pay for an
 * eager import if they're not using FKN at all.
 */
export const fknPolyfillHost = (lib: FknPolyfillModule): TorrentHost => ({
  dialTcp: ({ address, port }) =>
    new Promise<TcpConn>((resolve, reject) => {
      const sock = new lib.net.Socket()
      sock.once('connect', () => resolve(wrapNetSocket(sock)))
      sock.once('error', (err) => reject(err))
      sock.connect({ host: address, port })
    }),
  listenTcp: ({ address, port }) =>
    new Promise<TcpListener>((resolve, reject) => {
      const server = new lib.net.Server()
      const pending: TcpConn[] = []
      const waiters: Array<(c: TcpConn | null) => void> = []
      let closed = false

      server.on('connection', (sock: NetSocket) => {
        if (closed) {
          sock.destroy()
          return
        }
        const c = wrapNetSocket(sock)
        const w = waiters.shift()
        if (w) w(c)
        else pending.push(c)
      })
      server.once('error', (err) => {
        if (waiters.length === 0) reject(err)
        else while (waiters.length) waiters.shift()!(null)
      })
      server.once('listening', () => {
        const addr = server.address()
        const localAddress = typeof addr === 'object' && addr ? addr.address : address
        const localPort = typeof addr === 'object' && addr ? addr.port : port
        const localFamily: IpFamily =
          typeof addr === 'object' && addr ? (addr.family as IpFamily) : 'IPv4'
        resolve({
          localAddress,
          localFamily,
          localPort,
          accept: () =>
            new Promise<TcpConn | null>((res) => {
              if (pending.length > 0) {
                res(pending.shift()!)
                return
              }
              if (closed) {
                res(null)
                return
              }
              waiters.push(res)
            }),
          close: () =>
            new Promise<void>((res) => {
              closed = true
              server.close(() => {
                while (waiters.length) waiters.shift()!(null)
                res()
              })
            }),
        })
      })
      server.listen(port, address)
    }),
  bindUdp: ({ network, address, port }) =>
    new Promise<UdpSocket>((resolve, reject) => {
      const sock = lib.dgram.createSocket(network)
      const datagramQueue: UdpDatagram[] = []
      let streamController: ReadableStreamDefaultController<UdpDatagram> | null = null
      let closed = false

      const dataReadableStream = new ReadableStream<UdpDatagram>({
        start(controller) {
          streamController = controller
          while (datagramQueue.length) controller.enqueue(datagramQueue.shift()!)
        },
        cancel() {
          closed = true
          sock.close()
        },
      })

      sock.on('message', (data: Uint8Array | Buffer, rinfo: { address: string; port: number; family: 'IPv4' | 'IPv6' }) => {
        const buf: Uint8Array =
          data instanceof Uint8Array ? data : new Uint8Array(data as unknown as ArrayBufferLike)
        const dg: UdpDatagram = {
          data: buf,
          address: rinfo.address,
          port: rinfo.port,
          family: rinfo.family,
        }
        if (streamController) streamController.enqueue(dg)
        else datagramQueue.push(dg)
      })
      sock.once('error', (err: Error) => {
        if (!streamController) reject(err)
        else streamController.error(err)
      })
      sock.once('listening', () => {
        const addr = sock.address()
        resolve({
          localAddress: addr.address,
          localFamily: addr.family as IpFamily,
          localPort: addr.port,
          dataReadableStream,
          send: ({ message, address: a, port: p }) =>
            new Promise<void>((res, rej) => {
              const u8 = new Uint8Array(message)
              sock.send(u8, p, a, (err) => (err ? rej(err) : res()))
            }),
          close: () =>
            new Promise<void>((res) => {
              if (closed) {
                res()
                return
              }
              closed = true
              sock.close(() => res())
            }),
        })
      })
      sock.bind(port, address || undefined)
    }),
})

const wrapNetSocket = (sock: NetSocket): TcpConn => {
  const localFamily: IpFamily = (sock.localFamily as IpFamily) ?? 'IPv4'
  const remoteFamily: IpFamily = (sock.remoteFamily as IpFamily) ?? 'IPv4'

  // Bridge Node 'data'/'end'/'error' events into a WHATWG ReadableStream.
  const dataReadableStream = new ReadableStream<Uint8Array>({
    start(controller) {
      const onData = (chunk: unknown) => {
        const u8 =
          chunk instanceof Uint8Array
            ? chunk
            : new Uint8Array((chunk as { buffer: ArrayBufferLike }).buffer ?? (chunk as ArrayBuffer))
        controller.enqueue(u8)
      }
      const onEnd = () => {
        try {
          controller.close()
        } catch {}
      }
      const onError = (err: Error) => {
        try {
          controller.error(err)
        } catch {}
      }
      sock.on('data', onData)
      sock.on('end', onEnd)
      sock.on('error', onError)
      // Save handlers so cancel can remove them — but a teardown that closes
      // the socket is sufficient here.
    },
    cancel() {
      sock.destroy()
    },
  })

  const dataWritableStream = new WritableStream<Uint8Array>({
    write(chunk) {
      return new Promise<void>((resolve, reject) => {
        const ok = sock.write(chunk, (err) => (err ? reject(err) : resolve()))
        if (ok) resolve()
      })
    },
    close() {
      return new Promise<void>((res) => {
        sock.end(() => res())
      })
    },
    abort(reason) {
      sock.destroy(reason instanceof Error ? reason : new Error(String(reason)))
    },
  })

  return {
    localAddress: sock.localAddress ?? '',
    localFamily,
    localPort: sock.localPort ?? 0,
    remoteAddress: sock.remoteAddress ?? '',
    remoteFamily,
    remotePort: sock.remotePort ?? 0,
    dataReadableStream,
    dataWritableStream,
    close: () =>
      new Promise<void>((res) => {
        sock.end(() => res())
      }),
  }
}
