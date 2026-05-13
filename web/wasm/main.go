// Command torrent-wasm is the GOOS=js GOARCH=wasm entry point for the
// browser TypeScript wrapper under web/ts.
//
// It exposes a tiny imperative API on globalThis.__torrent that the TS
// wrapper drives. All networking and storage is delegated to
// globalThis.__torrentBridge (installed by the TS host before the
// runtime starts).

//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall/js"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/web/jsbridge"
)

func main() {
	jsbridge.Install()

	exports := js.Global().Get("Object").New()
	register := func(name string, fn func(this js.Value, args []js.Value) any) {
		exports.Set(name, js.FuncOf(fn))
	}

	register("newClient", jsNewClient)
	register("closeClient", jsCloseClient)
	register("addMagnet", jsAddMagnet)
	register("addTorrent", jsAddTorrent)
	register("torrentGotInfo", jsTorrentGotInfo)
	register("torrentInfo", jsTorrentInfo)
	register("torrentFiles", jsTorrentFiles)
	register("torrentStats", jsTorrentStats)
	register("torrentDownloadAll", jsTorrentDownloadAll)
	register("torrentDrop", jsTorrentDrop)
	register("fileRead", jsFileRead)

	js.Global().Set("__torrent", exports)

	// Signal readiness to the TS host.
	bridge := js.Global().Get("__torrentBridge")
	if !bridge.IsUndefined() && !bridge.IsNull() {
		onReady := bridge.Get("onReady")
		if onReady.Type() == js.TypeFunction {
			onReady.Invoke()
		}
	}

	// Block forever; the runtime will be terminated by the host or by
	// calling exit() from JS.
	select {}
}

// ---------------------------------------------------------------------
// Handle tables: opaque numeric IDs for Client/Torrent/File handles
// passed across the JS boundary.

type handles[T any] struct {
	mu  sync.Mutex
	m   map[uint64]T
	seq atomic.Uint64
}

func newHandles[T any]() *handles[T] { return &handles[T]{m: map[uint64]T{}} }

func (h *handles[T]) put(v T) uint64 {
	id := h.seq.Add(1)
	h.mu.Lock()
	h.m[id] = v
	h.mu.Unlock()
	return id
}

func (h *handles[T]) get(id uint64) (T, bool) {
	h.mu.Lock()
	v, ok := h.m[id]
	h.mu.Unlock()
	return v, ok
}

func (h *handles[T]) delete(id uint64) {
	h.mu.Lock()
	delete(h.m, id)
	h.mu.Unlock()
}

var (
	clients  = newHandles[*torrent.Client]()
	torrents = newHandles[*torrent.Torrent]()
	files    = newHandles[*torrent.File]()
)

// ---------------------------------------------------------------------
// JS bridge helpers.

// asPromise wraps a function that may block in a JS Promise. The function
// runs on a fresh goroutine. resolve receives the success value; reject
// receives the error.
func asPromise(fn func() (any, error)) js.Value {
	handler := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve := args[0]
		reject := args[1]
		go func() {
			defer func() {
				if r := recover(); r != nil {
					reject.Invoke(jsError(fmt.Errorf("panic: %v", r)))
				}
			}()
			v, err := fn()
			if err != nil {
				reject.Invoke(jsError(err))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(handler)
}

func jsError(err error) js.Value {
	return js.Global().Get("Error").New(err.Error())
}

func argUint64(args []js.Value, i int) (uint64, error) {
	if i >= len(args) {
		return 0, fmt.Errorf("missing arg %d", i)
	}
	return uint64(args[i].Float()), nil
}

func argString(args []js.Value, i int) (string, error) {
	if i >= len(args) || args[i].Type() != js.TypeString {
		return "", fmt.Errorf("expected string at arg %d", i)
	}
	return args[i].String(), nil
}

// ---------------------------------------------------------------------
// Client API

func jsNewClient(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		var opts js.Value
		if len(args) > 0 {
			opts = args[0]
		}
		cfg := torrent.NewDefaultClientConfig()
		cfg.DefaultStorage = jsbridge.NewStorage()

		// Route HTTP and tracker traffic through the bridge so we avoid the
		// browser fetch path (which is subject to CORS) and instead use raw
		// TCP via @fkn/lib's `net` polyfill. UDP trackers + DHT use the same
		// bridge through ListenPacket.
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return jsbridge.DialJsConn(ctx, network, addr)
		}
		listenPacket := func(network, addr string) (net.PacketConn, error) {
			return jsbridge.ListenPacketJS(network, addr)
		}
		cfg.HTTPDialContext = dial
		cfg.TrackerDialContext = dial
		cfg.TrackerListenPacket = listenPacket

		// Map a small subset of options.
		if opts.Type() == js.TypeObject {
			if v := opts.Get("disableTrackers"); v.Truthy() {
				cfg.DisableTrackers = true
			}
			if v := opts.Get("disablePEX"); v.Truthy() {
				cfg.DisablePEX = true
			}
			if v := opts.Get("disableDHT"); v.Truthy() {
				cfg.NoDHT = true
			}
			if v := opts.Get("disableTCP"); v.Truthy() {
				cfg.DisableTCP = true
			}
			if v := opts.Get("disableUTP"); v.Truthy() {
				cfg.DisableUTP = true
			}
			if v := opts.Get("seed"); v.Truthy() {
				cfg.Seed = true
			}
			if v := opts.Get("listenPort"); v.Type() == js.TypeNumber {
				cfg.ListenPort = v.Int()
			}
			if v := opts.Get("peerID"); v.Type() == js.TypeString {
				cfg.PeerID = v.String()
			}
			if v := opts.Get("debug"); v.Truthy() {
				cfg.Debug = true
				slog.SetLogLoggerLevel(slog.LevelDebug)
			}
		}

		cl, err := torrent.NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("NewClient: %w", err)
		}
		id := clients.put(cl)
		return js.ValueOf(map[string]any{"id": float64(id)}), nil
	})
}

func jsCloseClient(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		id, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		cl, ok := clients.get(id)
		if !ok {
			return nil, errors.New("unknown client id")
		}
		errs := cl.Close()
		clients.delete(id)
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return js.Undefined(), nil
	})
}

func jsAddMagnet(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		clientID, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		uri, err := argString(args, 1)
		if err != nil {
			return nil, err
		}
		cl, ok := clients.get(clientID)
		if !ok {
			return nil, errors.New("unknown client id")
		}
		t, err := cl.AddMagnet(uri)
		if err != nil {
			return nil, fmt.Errorf("AddMagnet: %w", err)
		}
		tid := torrents.put(t)
		return js.ValueOf(map[string]any{
			"id":       float64(tid),
			"infoHash": t.InfoHash().HexString(),
		}), nil
	})
}

func jsAddTorrent(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		clientID, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		if len(args) < 2 {
			return nil, errors.New("missing metainfo bytes")
		}
		bytesArg := args[1]
		buf := make([]byte, bytesArg.Get("byteLength").Int())
		js.CopyBytesToGo(buf, bytesArg)
		cl, ok := clients.get(clientID)
		if !ok {
			return nil, errors.New("unknown client id")
		}
		mi, err := metainfoFromBytes(buf)
		if err != nil {
			return nil, err
		}
		t, err := cl.AddTorrent(mi)
		if err != nil {
			return nil, fmt.Errorf("AddTorrent: %w", err)
		}
		tid := torrents.put(t)
		return js.ValueOf(map[string]any{
			"id":       float64(tid),
			"infoHash": t.InfoHash().HexString(),
		}), nil
	})
}

func jsTorrentGotInfo(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		<-t.GotInfo()
		return js.Undefined(), nil
	})
}

func jsTorrentInfo(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		info := t.Info()
		if info == nil {
			return nil, errors.New("info not yet available")
		}
		return js.ValueOf(map[string]any{
			"name":        t.Name(),
			"infoHash":    t.InfoHash().HexString(),
			"length":      float64(t.Length()),
			"pieceLength": float64(info.PieceLength),
			"numPieces":   float64(t.NumPieces()),
		}), nil
	})
}

func jsTorrentFiles(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		fs := t.Files()
		arr := make([]any, 0, len(fs))
		for _, f := range fs {
			fid := files.put(f)
			arr = append(arr, map[string]any{
				"id":     float64(fid),
				"path":   f.DisplayPath(),
				"length": float64(f.Length()),
				"offset": float64(f.Offset()),
			})
		}
		return js.ValueOf(arr), nil
	})
}

func jsTorrentStats(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		st := t.Stats()
		return js.ValueOf(map[string]any{
			"bytesCompleted":  float64(t.BytesCompleted()),
			"bytesMissing":    float64(t.BytesMissing()),
			"activePeers":     float64(st.ActivePeers),
			"connectedSeeders": float64(st.ConnectedSeeders),
			"totalPeers":      float64(st.TotalPeers),
			"halfOpenPeers":   float64(st.HalfOpenPeers),
		}), nil
	})
}

func jsTorrentDownloadAll(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		t.DownloadAll()
		return js.Undefined(), nil
	})
}

func jsTorrentDrop(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		tid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		t, ok := torrents.get(tid)
		if !ok {
			return nil, errors.New("unknown torrent id")
		}
		t.Drop()
		torrents.delete(tid)
		return js.Undefined(), nil
	})
}

// jsFileRead reads up to len bytes from the file's reader at the given
// offset. The torrent client streams data on-demand: the call blocks
// until enough pieces are downloaded.
func jsFileRead(this js.Value, args []js.Value) any {
	return asPromise(func() (any, error) {
		fid, err := argUint64(args, 0)
		if err != nil {
			return nil, err
		}
		off := int64(args[1].Float())
		length := int(args[2].Float())
		f, ok := files.get(fid)
		if !ok {
			return nil, errors.New("unknown file id")
		}
		r := f.NewReader()
		defer r.Close()
		_, err = r.Seek(off, io.SeekStart)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, length)
		n, err := io.ReadFull(r, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, err
		}
		out := js.Global().Get("Uint8Array").New(n)
		if n > 0 {
			js.CopyBytesToJS(out, buf[:n])
		}
		return out, nil
	})
}

// ---------------------------------------------------------------------
// Misc helpers

// metainfoFromBytes parses a .torrent file payload.
func metainfoFromBytes(b []byte) (*metainfo.MetaInfo, error) {
	return metainfo.Load(bytes.NewReader(b))
}
