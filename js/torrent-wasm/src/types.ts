// Types shared between the wasm bridge contract and the public client API.

export type IpFamily = 'IPv4' | 'IPv6'

export type TcpConn = {
  localAddress: string
  localFamily: IpFamily
  localPort: number
  remoteAddress: string
  remoteFamily: IpFamily
  remotePort: number
  dataReadableStream: ReadableStream<Uint8Array>
  dataWritableStream: WritableStream<Uint8Array>
  close: () => Promise<void>
}

export type TcpListener = {
  localAddress: string
  localFamily: IpFamily
  localPort: number
  accept: () => Promise<TcpConn | null>
  close: () => Promise<void>
}

export type UdpDatagram = {
  data: ArrayBuffer | Uint8Array
  address: string
  port: number
  family: IpFamily
}

export type UdpSocket = {
  localAddress: string
  localFamily: IpFamily
  localPort: number
  dataReadableStream: ReadableStream<UdpDatagram>
  send: (opts: { message: ArrayBuffer; address: string; port: number }) => Promise<void>
  close: () => Promise<void>
}

export type TorrentHost = {
  dialTcp: (opts: { network: 'tcp4' | 'tcp6'; address: string; port: number }) => Promise<TcpConn>
  listenTcp: (opts: { network: 'tcp4' | 'tcp6'; address: string; port: number }) => Promise<TcpListener>
  bindUdp: (opts: { network: 'udp4' | 'udp6'; address: string; port: number }) => Promise<UdpSocket>
}

export type StorageKind = 'memory' | 'opfs'

export type CreateClientOptions = {
  storage?: StorageKind
  opfsRoot?: FileSystemDirectoryHandle
  disableTcp?: boolean
  disableUtp?: boolean
  disableDht?: boolean
  disableTrackers?: boolean
  disablePex?: boolean
  disableWebtorrent?: boolean
  disableWebseeds?: boolean
  peerId?: string
  listenPort?: number
}

export type FileInfo = {
  index: number
  path: string[]
  displayPath: string
  length: number
  offset: number
  download: () => void
  createReadStream: (start?: number, end?: number) => ReadableStream<Uint8Array>
}

export type TorrentInfo = {
  name: string
  pieceLength: number
  totalLength: number
  numPieces: number
}

export type TorrentStats = {
  activePeers: number
  connectedSeeders: number
  totalPeers: number
  pendingPeers: number
}

export type TorrentProgress = {
  bytesCompleted: number
  totalLength: number
}

export type TorrentHandle = {
  readonly infoHash: string
  readonly name: string
  gotInfo: () => Promise<void>
  info: () => TorrentInfo | null
  files: () => FileInfo[]
  downloadAll: () => void
  stats: () => TorrentStats
  bytesCompleted: () => TorrentProgress
  drop: () => void
}

export type ClientHandle = {
  readonly peerId: string
  addMagnet: (uri: string) => Promise<TorrentHandle>
  addInfoHash: (hex: string) => Promise<TorrentHandle>
  torrents: () => TorrentHandle[]
  listenAddrs: () => string[]
  close: () => Promise<void>
}
