// Adapter that fulfils the TorrentHost contract using @fkn/lib's raw stream
// APIs (webVpnTcpSocket / webVpnTcpSocketListener / webVpnUdpSocket).
//
// Usage:
//   import { fknHost } from '@anacrolix/torrent-wasm/host-fkn'
//   await load({ wasm: '...', wasmExec: '...', host: await fknHost() })

import type {
  IpFamily,
  TcpConn,
  TcpListener,
  TorrentHost,
  UdpDatagram,
  UdpSocket,
} from './types.js'

type FknNet = {
  webVpnTcpSocket: (opts: { remoteAddress: string; remotePort: number }) => Promise<{
    localAddress: string
    localFamily: IpFamily
    localPort: number
    remoteAddress: string
    remoteFamily: IpFamily
    remotePort: number
    dataReadableStream: ReadableStream<Uint8Array>
    dataWritableStream: WritableStream<Uint8Array>
    end: () => Promise<void>
    destroy: () => Promise<void>
  }>
  webVpnTcpSocketListener: (opts: {
    localAddress: string
    localPort: number
    onConnection: (conn: Awaited<ReturnType<FknNet['webVpnTcpSocket']>>) => void
  }) => Promise<{
    localAddress: string
    localFamily: IpFamily
    localPort: number
    close: () => Promise<void>
  }>
  webVpnUdpSocket: (opts: { type: 'udp4' | 'udp6'; port: number; address: string }) => Promise<{
    localAddress: string
    localFamily: IpFamily
    localPort: number
    dataReadableStream: ReadableStream<{
      data: ArrayBuffer
      size: number
      family: IpFamily
      address: string
      port: number
    }>
    connect: (opts: { remoteAddress: string; remotePort: number }) => Promise<void>
    send: (opts: { message: ArrayBuffer; address?: string; port?: number }) => Promise<void>
    close: () => Promise<void>
  }>
}

/**
 * Builds a TorrentHost from @fkn/lib. We import @fkn/lib dynamically so the
 * adapter can be tree-shaken away when callers supply their own host.
 */
export const fknHost = async (overrides?: Partial<FknNet>): Promise<TorrentHost> => {
  let api: FknNet
  if (
    overrides
    && typeof overrides.webVpnTcpSocket === 'function'
    && typeof overrides.webVpnTcpSocketListener === 'function'
    && typeof overrides.webVpnUdpSocket === 'function'
  ) {
    api = overrides as FknNet
  } else {
    const mod = await import('@fkn/lib' as string)
    const candidate = (mod as unknown as { webVpnTcpSocket?: FknNet['webVpnTcpSocket'] })
    if (!candidate.webVpnTcpSocket) {
      throw new Error(
        '@fkn/lib does not expose the raw webVpn* functions. Re-export them or pass them via overrides.',
      )
    }
    api = mod as unknown as FknNet
    if (overrides) {
      api = { ...api, ...overrides } as FknNet
    }
  }

  return makeHostFromFkn(api)
}

const makeHostFromFkn = (api: FknNet): TorrentHost => ({
  dialTcp: async ({ network, address, port }) => {
    const conn = await api.webVpnTcpSocket({ remoteAddress: address, remotePort: port })
    return wrapFknTcpConn(conn)
  },
  listenTcp: async ({ network, address, port }) => {
    // The FKN listener uses an onConnection callback. Convert to a pull-style
    // accept() backed by a FIFO so the Go side can ask for the next one.
    const pending: TcpConn[] = []
    const waiters: Array<(c: TcpConn | null) => void> = []
    let closed = false

    const enqueue = (conn: TcpConn) => {
      const w = waiters.shift()
      if (w) {
        w(conn)
      } else {
        pending.push(conn)
      }
    }

    const handle = await api.webVpnTcpSocketListener({
      localAddress: address,
      localPort: port,
      onConnection: (raw) => {
        if (closed) return
        enqueue(wrapFknTcpConn(raw))
      },
    })

    const result: TcpListener = {
      localAddress: handle.localAddress,
      localFamily: handle.localFamily,
      localPort: handle.localPort,
      accept: () =>
        new Promise((resolve) => {
          if (pending.length > 0) {
            resolve(pending.shift()!)
            return
          }
          if (closed) {
            resolve(null)
            return
          }
          waiters.push(resolve)
        }),
      close: async () => {
        closed = true
        await handle.close()
        while (waiters.length) waiters.shift()!(null)
      },
    }
    return result
  },
  bindUdp: async ({ network, address, port }) => {
    const sock = await api.webVpnUdpSocket({
      type: network,
      address,
      port,
    })
    // Adapt the data stream: FKN already shapes datagrams as
    // { data: ArrayBuffer, size, family, address, port }, which matches the
    // contract we need.
    const adapted = sock.dataReadableStream.pipeThrough(
      new TransformStream<{ data: ArrayBuffer; size: number; family: IpFamily; address: string; port: number }, UdpDatagram>({
        transform(chunk, controller) {
          controller.enqueue({
            data: chunk.data,
            address: chunk.address,
            port: chunk.port,
            family: chunk.family,
          })
        },
      }),
    )
    const out: UdpSocket = {
      localAddress: sock.localAddress,
      localFamily: sock.localFamily,
      localPort: sock.localPort,
      dataReadableStream: adapted,
      send: ({ message, address: addr, port: p }) => sock.send({ message, address: addr, port: p }),
      close: () => sock.close(),
    }
    return out
  },
})

const wrapFknTcpConn = (
  conn: Awaited<ReturnType<FknNet['webVpnTcpSocket']>>,
): TcpConn => ({
  localAddress: conn.localAddress,
  localFamily: conn.localFamily,
  localPort: conn.localPort,
  remoteAddress: conn.remoteAddress,
  remoteFamily: conn.remoteFamily,
  remotePort: conn.remotePort,
  dataReadableStream: conn.dataReadableStream,
  dataWritableStream: conn.dataWritableStream,
  close: async () => {
    try {
      await conn.end()
    } catch {
      await conn.destroy()
    }
  },
})
