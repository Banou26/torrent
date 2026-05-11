//go:build js && wasm

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall/js"

	"github.com/anacrolix/torrent/metainfo"
)

// OpfsClientOptions configures the OPFS-backed storage. RootDirHandle is the
// JS-side FileSystemDirectoryHandle under which per-torrent subdirectories
// will be created.
type OpfsClientOptions struct {
	// RootDirHandle is a FileSystemDirectoryHandle obtained from JS, e.g.
	// `await navigator.storage.getDirectory()`. Required.
	RootDirHandle js.Value
	// PieceCompletion is the persistence layer for piece completion. Defaults
	// to in-memory (NewMapPieceCompletion).
	PieceCompletion PieceCompletion
}

// NewOpfs returns a ClientImpl that stores torrents in the Origin Private
// File System via a JS-supplied FileSystemDirectoryHandle. Each torrent gets
// a subdirectory (named by hex infohash) containing one file per piece.
//
// All I/O is async on the JS side; Go-side ReadAt/WriteAt block on the
// resulting promises. This works fine for streaming consumers because the
// torrent client already dispatches I/O to goroutines.
func NewOpfs(opts OpfsClientOptions) (ClientImplCloser, error) {
	if opts.RootDirHandle.IsUndefined() || opts.RootDirHandle.IsNull() {
		return nil, errors.New("opfs storage: RootDirHandle is required")
	}
	if opts.PieceCompletion == nil {
		opts.PieceCompletion = NewMapPieceCompletion()
	}
	return &opfsClient{
		root:       opts.RootDirHandle,
		completion: opts.PieceCompletion,
	}, nil
}

type opfsClient struct {
	root       js.Value
	completion PieceCompletion
}

func (c *opfsClient) Close() error {
	return c.completion.Close()
}

func (c *opfsClient) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	// Get / create the torrent subdirectory.
	dirName := infoHash.HexString()
	dirHandle, err := awaitJS(c.root.Call("getDirectoryHandle", dirName, mkObj("create", true)))
	if err != nil {
		return TorrentImpl{}, fmt.Errorf("opfs getDirectoryHandle: %w", err)
	}

	t := &opfsTorrent{
		client:    c,
		dir:       dirHandle,
		infoHash:  infoHash,
		pieceLen:  info.PieceLength,
		numPieces: info.NumPieces(),
		pieces:    make([]opfsPiece, info.NumPieces()),
	}
	for i := range t.pieces {
		t.pieces[i].torrent = t
		t.pieces[i].index = i
		t.pieces[i].length = info.Piece(i).Length()
	}
	return TorrentImpl{
		Piece: t.Piece,
		Close: t.Close,
	}, nil
}

type opfsTorrent struct {
	client    *opfsClient
	dir       js.Value
	infoHash  metainfo.Hash
	pieceLen  int64
	numPieces int
	pieces    []opfsPiece
}

func (t *opfsTorrent) Piece(p metainfo.Piece) PieceImpl {
	return &t.pieces[p.Index()]
}

func (t *opfsTorrent) Close() error { return nil }

type opfsPiece struct {
	torrent *opfsTorrent
	index   int
	length  int64

	mu     sync.Mutex
	handle js.Value
}

func (p *opfsPiece) fileName() string {
	var b strings.Builder
	fmt.Fprintf(&b, "piece-%010d", p.index)
	return b.String()
}

func (p *opfsPiece) ensureHandleLocked() (js.Value, error) {
	if !p.handle.IsUndefined() && !p.handle.IsNull() {
		return p.handle, nil
	}
	h, err := awaitJS(p.torrent.dir.Call("getFileHandle", p.fileName(), mkObj("create", true)))
	if err != nil {
		return js.Undefined(), fmt.Errorf("getFileHandle: %w", err)
	}
	p.handle = h
	return h, nil
}

func (p *opfsPiece) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.length {
		return 0, io.EOF
	}
	p.mu.Lock()
	h, err := p.ensureHandleLocked()
	p.mu.Unlock()
	if err != nil {
		return 0, err
	}
	fileVal, err := awaitJS(h.Call("getFile"))
	if err != nil {
		return 0, fmt.Errorf("getFile: %w", err)
	}
	// Slice the relevant range and read it as ArrayBuffer.
	end := off + int64(len(b))
	if end > p.length {
		end = p.length
	}
	if end <= off {
		return 0, io.EOF
	}
	sliced := fileVal.Call("slice", off, end)
	arrayBuf, err := awaitJS(sliced.Call("arrayBuffer"))
	if err != nil {
		return 0, fmt.Errorf("arrayBuffer: %w", err)
	}
	view := js.Global().Get("Uint8Array").New(arrayBuf)
	n := view.Get("byteLength").Int()
	if n == 0 {
		return 0, io.EOF
	}
	js.CopyBytesToGo(b[:n], view)
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *opfsPiece) WriteAt(b []byte, off int64) (int, error) {
	if off+int64(len(b)) > p.length {
		return 0, errors.New("opfs piece: write past end")
	}
	p.mu.Lock()
	h, err := p.ensureHandleLocked()
	p.mu.Unlock()
	if err != nil {
		return 0, err
	}
	writable, err := awaitJS(h.Call("createWritable", mkObj("keepExistingData", true)))
	if err != nil {
		return 0, fmt.Errorf("createWritable: %w", err)
	}
	// Build a {position, type:'write', data} record. We must pad up to off if
	// the file is shorter — OPFS createWritable doesn't auto-pad.
	chunk := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(chunk, b)
	if _, err := awaitJS(writable.Call("seek", off)); err != nil {
		_ = writable.Call("close")
		return 0, fmt.Errorf("seek: %w", err)
	}
	if _, err := awaitJS(writable.Call("write", chunk)); err != nil {
		_ = writable.Call("close")
		return 0, fmt.Errorf("write: %w", err)
	}
	if _, err := awaitJS(writable.Call("close")); err != nil {
		return 0, fmt.Errorf("close: %w", err)
	}
	return len(b), nil
}

func (p *opfsPiece) MarkComplete() error {
	return p.torrent.client.completion.Set(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	}, true)
}

func (p *opfsPiece) MarkNotComplete() error {
	p.mu.Lock()
	h := p.handle
	p.handle = js.Value{}
	p.mu.Unlock()
	if !h.IsUndefined() && !h.IsNull() {
		// Best-effort delete by recreating an empty file via removeEntry.
		_, _ = awaitJS(p.torrent.dir.Call("removeEntry", p.fileName()))
	}
	return p.torrent.client.completion.Set(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	}, false)
}

func (p *opfsPiece) Completion() Completion {
	c, err := p.torrent.client.completion.Get(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	})
	c.Err = err
	return c
}

// --- JS helpers (duplicated from webvpnbridge to avoid an import cycle) ---

func awaitJS(promise js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	var resolve, reject js.Func
	resolve = js.FuncOf(func(this js.Value, args []js.Value) any {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{v: v}
		return nil
	})
	reject = js.FuncOf(func(this js.Value, args []js.Value) any {
		msg := "promise rejected"
		if len(args) > 0 {
			if m := args[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			} else if args[0].Type() == js.TypeString {
				msg = args[0].String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	promise.Call("then", resolve, reject)
	r := <-ch
	resolve.Release()
	reject.Release()
	return r.v, r.err
}

func mkObj(kvs ...any) js.Value {
	o := js.Global().Get("Object").New()
	for i := 0; i+1 < len(kvs); i += 2 {
		k, ok := kvs[i].(string)
		if !ok {
			continue
		}
		o.Set(k, kvs[i+1])
	}
	return o
}
