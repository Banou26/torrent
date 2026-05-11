//go:build js && wasm

// Command torrent-wasm exposes the anacrolix/torrent client as a JS API.
// The expected boot sequence is:
//
//  1. JS installs globalThis.__torrent_host (see internal/webvpnbridge).
//  2. JS runs torrent.wasm via Go's standard wasm_exec.js loader.
//  3. The Go main installs globalThis.__torrent which exposes the API.
//  4. JS calls __torrent.createClient(options) and goes from there.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/webvpnbridge"
	"github.com/anacrolix/torrent/storage"
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("createClient", js.FuncOf(jsCreateClient))
	js.Global().Set("__torrent", api)

	// Signal readiness to JS via a deferred promise it can resolve on.
	if ready := js.Global().Get("__torrent_on_ready"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}

	// Block forever — keep callbacks alive.
	select {}
}

// jsCreateClient(options) -> Promise<ClientHandle>
//
// options: {
//   storage?: 'memory' | 'opfs',
//   opfsRoot?: FileSystemDirectoryHandle,
//   disableTcp?: bool,
//   disableUtp?: bool,
//   disableDht?: bool,
//   disableTrackers?: bool,
//   disablePex?: bool,
//   disableWebtorrent?: bool,
//   peerId?: string,
//   listenPort?: number,
// }
func jsCreateClient(this js.Value, args []js.Value) any {
	var opts js.Value
	if len(args) > 0 {
		opts = args[0]
	}
	return jsPromise(func() (js.Value, error) {
		cl, err := buildClient(opts)
		if err != nil {
			return js.Undefined(), err
		}
		return newClientHandle(cl).jsObject(), nil
	})
}

func buildClient(opts js.Value) (*torrent.Client, error) {
	cfg := torrent.NewDefaultClientConfig()
	// In the browser there's no public-IP local listener — disable port
	// forwarding by default and trust the JS host to handle inbound.
	cfg.NoDefaultPortForwarding = true

	// All network-related defaults need to go through the JS bridge.
	cfg.TrackerListenPacket = func(network, addr string) (net.PacketConn, error) {
		return webvpnbridge.BindUDP(network, addr)
	}
	cfg.HTTPDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return webvpnbridge.DialTCP(ctx, network, addr)
	}
	cfg.TrackerDialContext = cfg.HTTPDialContext
	// Replace the default HTTP RoundTripper so trackers, webseeds, and
	// metainfo sources bypass the browser fetch() (which is CORS-restricted)
	// and tunnel through the WebVPN.
	cfg.WebTransport = &http.Transport{
		DialContext:           cfg.HTTPDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	// Storage selection.
	storageKind := optString(opts, "storage", "memory")
	switch storageKind {
	case "memory":
		cfg.DefaultStorage = storage.NewMemory()
	case "opfs":
		root := opts.Get("opfsRoot")
		if root.IsUndefined() || root.IsNull() {
			return nil, errors.New("opfs storage requires options.opfsRoot")
		}
		s, err := storage.NewOpfs(storage.OpfsClientOptions{RootDirHandle: root})
		if err != nil {
			return nil, err
		}
		cfg.DefaultStorage = s
	default:
		return nil, fmt.Errorf("unknown storage kind %q", storageKind)
	}

	// Booleans.
	cfg.DisableTCP = optBool(opts, "disableTcp", false)
	cfg.DisableUTP = optBool(opts, "disableUtp", false)
	cfg.NoDHT = optBool(opts, "disableDht", false)
	cfg.DisableTrackers = optBool(opts, "disableTrackers", false)
	cfg.DisablePEX = optBool(opts, "disablePex", false)
	cfg.DisableWebtorrent = optBool(opts, "disableWebtorrent", false)
	cfg.DisableWebseeds = optBool(opts, "disableWebseeds", false)

	if port := opts.Get("listenPort"); port.Type() == js.TypeNumber {
		cfg.ListenPort = port.Int()
	} else {
		// Random ephemeral port in the typical BT range.
		cfg.ListenPort = 0
	}
	if peerID := opts.Get("peerId"); peerID.Type() == js.TypeString {
		cfg.PeerID = peerID.String()
	}

	return torrent.NewClient(cfg)
}

func optBool(v js.Value, key string, def bool) bool {
	if v.IsUndefined() || v.IsNull() {
		return def
	}
	f := v.Get(key)
	if f.Type() != js.TypeBoolean {
		return def
	}
	return f.Bool()
}

func optString(v js.Value, key, def string) string {
	if v.IsUndefined() || v.IsNull() {
		return def
	}
	f := v.Get(key)
	if f.Type() != js.TypeString {
		return def
	}
	return f.String()
}

// --- Client handle ---------------------------------------------------------

type clientHandle struct {
	cl        *torrent.Client
	torrentMu sync.Mutex
	torrents  map[string]*torrentHandle // by infohash hex
}

func newClientHandle(cl *torrent.Client) *clientHandle {
	return &clientHandle{cl: cl, torrents: map[string]*torrentHandle{}}
}

func (c *clientHandle) jsObject() js.Value {
	o := js.Global().Get("Object").New()
	o.Set("addMagnet", js.FuncOf(c.addMagnet))
	o.Set("addInfoHash", js.FuncOf(c.addInfoHash))
	o.Set("torrents", js.FuncOf(c.listTorrents))
	o.Set("close", js.FuncOf(c.close))
	o.Set("listenAddrs", js.FuncOf(c.listenAddrs))
	pid := c.cl.PeerID()
	o.Set("peerId", js.ValueOf(fmt.Sprintf("%x", pid[:])))
	return o
}

func (c *clientHandle) addMagnet(this js.Value, args []js.Value) any {
	if len(args) == 0 || args[0].Type() != js.TypeString {
		return jsRejected(errors.New("addMagnet: missing magnet URI"))
	}
	uri := args[0].String()
	return jsPromise(func() (js.Value, error) {
		t, err := c.cl.AddMagnet(uri)
		if err != nil {
			return js.Undefined(), err
		}
		return c.registerTorrent(t).jsObject(), nil
	})
}

func (c *clientHandle) addInfoHash(this js.Value, args []js.Value) any {
	if len(args) == 0 || args[0].Type() != js.TypeString {
		return jsRejected(errors.New("addInfoHash: missing infohash hex string"))
	}
	hashStr := args[0].String()
	return jsPromise(func() (js.Value, error) {
		spec := &torrent.TorrentSpec{}
		var ih [20]byte
		n, err := fmt.Sscanf(hashStr, "%40x", &ih)
		if err != nil || n != 1 {
			return js.Undefined(), fmt.Errorf("invalid infohash %q", hashStr)
		}
		spec.InfoHash = ih
		t, _, err := c.cl.AddTorrentSpec(spec)
		if err != nil {
			return js.Undefined(), err
		}
		return c.registerTorrent(t).jsObject(), nil
	})
}

func (c *clientHandle) listTorrents(this js.Value, args []js.Value) any {
	c.torrentMu.Lock()
	defer c.torrentMu.Unlock()
	arr := js.Global().Get("Array").New()
	for _, t := range c.torrents {
		arr.Call("push", t.jsObject())
	}
	return arr
}

func (c *clientHandle) close(this js.Value, args []js.Value) any {
	return jsPromise(func() (js.Value, error) {
		errs := c.cl.Close()
		if len(errs) > 0 {
			return js.Undefined(), errors.Join(errs...)
		}
		return js.Undefined(), nil
	})
}

func (c *clientHandle) listenAddrs(this js.Value, args []js.Value) any {
	arr := js.Global().Get("Array").New()
	for _, a := range c.cl.ListenAddrs() {
		arr.Call("push", js.ValueOf(a.String()))
	}
	return arr
}

func (c *clientHandle) registerTorrent(t *torrent.Torrent) *torrentHandle {
	key := t.InfoHash().HexString()
	c.torrentMu.Lock()
	defer c.torrentMu.Unlock()
	if existing, ok := c.torrents[key]; ok {
		return existing
	}
	th := &torrentHandle{t: t}
	c.torrents[key] = th
	return th
}

// --- Torrent handle --------------------------------------------------------

type torrentHandle struct {
	t *torrent.Torrent
}

func (th *torrentHandle) jsObject() js.Value {
	o := js.Global().Get("Object").New()
	o.Set("infoHash", js.ValueOf(th.t.InfoHash().HexString()))
	o.Set("name", js.ValueOf(th.t.Name()))
	o.Set("gotInfo", js.FuncOf(th.gotInfo))
	o.Set("info", js.FuncOf(th.info))
	o.Set("files", js.FuncOf(th.files))
	o.Set("downloadAll", js.FuncOf(th.downloadAll))
	o.Set("stats", js.FuncOf(th.stats))
	o.Set("bytesCompleted", js.FuncOf(th.bytesCompleted))
	o.Set("drop", js.FuncOf(th.drop))
	return o
}

func (th *torrentHandle) gotInfo(this js.Value, args []js.Value) any {
	return jsPromise(func() (js.Value, error) {
		select {
		case <-th.t.GotInfo():
			return js.Undefined(), nil
		case <-th.t.Closed():
			return js.Undefined(), errors.New("torrent closed before info was received")
		}
	})
}

func (th *torrentHandle) info(this js.Value, args []js.Value) any {
	info := th.t.Info()
	if info == nil {
		return js.Null()
	}
	o := js.Global().Get("Object").New()
	o.Set("name", js.ValueOf(info.BestName()))
	o.Set("pieceLength", js.ValueOf(info.PieceLength))
	o.Set("totalLength", js.ValueOf(info.TotalLength()))
	o.Set("numPieces", js.ValueOf(info.NumPieces()))
	return o
}

func (th *torrentHandle) files(this js.Value, args []js.Value) any {
	files := th.t.Files()
	arr := js.Global().Get("Array").New()
	for i, f := range files {
		entry := js.Global().Get("Object").New()
		entry.Set("index", js.ValueOf(i))
		entry.Set("path", js.ValueOf(f.Path()))
		entry.Set("displayPath", js.ValueOf(f.DisplayPath()))
		entry.Set("length", js.ValueOf(f.Length()))
		entry.Set("offset", js.ValueOf(f.Offset()))
		entry.Set("download", makeFileDownload(f))
		entry.Set("createReadStream", makeFileReadStream(f))
		arr.Call("push", entry)
	}
	return arr
}

func makeFileDownload(f *torrent.File) js.Func {
	var fn js.Func
	fn = js.FuncOf(func(this js.Value, args []js.Value) any {
		f.Download()
		return js.Undefined()
	})
	return fn
}

func makeFileReadStream(f *torrent.File) js.Func {
	var fn js.Func
	fn = js.FuncOf(func(this js.Value, args []js.Value) any {
		start, end := int64(0), f.Length()
		if len(args) > 0 && args[0].Type() == js.TypeNumber {
			start = int64(args[0].Float())
		}
		if len(args) > 1 && args[1].Type() == js.TypeNumber {
			end = int64(args[1].Float())
		}
		if start < 0 {
			start = 0
		}
		if end > f.Length() {
			end = f.Length()
		}
		if start >= end {
			return makeEmptyReadableStream()
		}
		return makeReadableStreamFromReader(f, start, end)
	})
	return fn
}

func makeEmptyReadableStream() js.Value {
	src := js.Global().Get("Object").New()
	src.Set("start", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("close")
		}
		return js.Undefined()
	}))
	return js.Global().Get("ReadableStream").New(src)
}

// makeReadableStreamFromReader constructs a JS ReadableStream that pulls
// chunks from the torrent file via a Go-side Reader. The stream's pull()
// callback dispatches a goroutine that reads the next chunk and enqueues it
// on the controller, satisfying backpressure: ReadableStream won't call
// pull() again until the consumer has requested more.
func makeReadableStreamFromReader(f *torrent.File, start, end int64) js.Value {
	chunkSize := int64(64 * 1024)

	state := &streamState{
		reader: f.NewReader(),
		start:  start,
		end:    end,
	}
	_, _ = state.reader.Seek(start, 0)
	state.pos = start

	src := js.Global().Get("Object").New()
	src.Set("pull", js.FuncOf(func(this js.Value, args []js.Value) any {
		controller := args[0]
		return jsPromise(func() (js.Value, error) {
			remaining := state.end - state.pos
			if remaining <= 0 {
				controller.Call("close")
				state.close()
				return js.Undefined(), nil
			}
			toRead := remaining
			if toRead > chunkSize {
				toRead = chunkSize
			}
			buf := make([]byte, toRead)
			n, err := state.reader.Read(buf)
			if n > 0 {
				state.pos += int64(n)
				jsBuf := js.Global().Get("Uint8Array").New(n)
				js.CopyBytesToJS(jsBuf, buf[:n])
				controller.Call("enqueue", jsBuf)
			}
			if err != nil {
				controller.Call("close")
				state.close()
			}
			return js.Undefined(), nil
		})
	}))
	src.Set("cancel", js.FuncOf(func(this js.Value, args []js.Value) any {
		state.close()
		return js.Undefined()
	}))
	return js.Global().Get("ReadableStream").New(src)
}

type streamState struct {
	reader    torrent.Reader
	start     int64
	pos       int64
	end       int64
	closeOnce atomic.Bool
}

func (s *streamState) close() {
	if s.closeOnce.CompareAndSwap(false, true) {
		_ = s.reader.Close()
	}
}

func (th *torrentHandle) downloadAll(this js.Value, args []js.Value) any {
	th.t.DownloadAll()
	return js.Undefined()
}

func (th *torrentHandle) stats(this js.Value, args []js.Value) any {
	s := th.t.Stats()
	o := js.Global().Get("Object").New()
	o.Set("activePeers", js.ValueOf(s.ActivePeers))
	o.Set("connectedSeeders", js.ValueOf(s.ConnectedSeeders))
	o.Set("totalPeers", js.ValueOf(s.TotalPeers))
	o.Set("pendingPeers", js.ValueOf(s.PendingPeers))
	return o
}

func (th *torrentHandle) bytesCompleted(this js.Value, args []js.Value) any {
	o := js.Global().Get("Object").New()
	o.Set("bytesCompleted", js.ValueOf(th.t.BytesCompleted()))
	o.Set("totalLength", js.ValueOf(th.t.Length()))
	return o
}

func (th *torrentHandle) drop(this js.Value, args []js.Value) any {
	th.t.Drop()
	return js.Undefined()
}

// --- Promise helpers -------------------------------------------------------

// jsPromise wraps a Go function returning (js.Value, error) into a JS
// Promise. The Promise constructor invokes the executor synchronously, so
// once Promise.new returns the executor func can be released — the resolve
// and reject handles live on inside the JS Promise.
func jsPromise(fn func() (js.Value, error)) js.Value {
	executor := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			v, err := fn()
			if err != nil {
				reject.Invoke(jsError(err))
			} else {
				resolve.Invoke(v)
			}
		}()
		return js.Undefined()
	})
	p := js.Global().Get("Promise").New(executor)
	executor.Release()
	return p
}

func jsRejected(err error) js.Value {
	return js.Global().Get("Promise").Call("reject", jsError(err))
}

func jsError(err error) js.Value {
	return js.Global().Get("Error").New(err.Error())
}
