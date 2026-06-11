//go:build js && wasm

package jsbridge

import (
	"context"
	"fmt"
	"io"
	"syscall/js"

	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Storage is a storage.ClientImpl that delegates all piece reads, writes,
// and completion state to the JS bridge. The bridge identifies torrents
// by an opaque numeric ID returned from `storageOpen`.
type Storage struct{}

// NewStorage returns a storage.ClientImpl backed by the JS bridge. The
// host must implement the matching methods on globalThis.__torrentBridge:
//
//	storageOpen(infoHash: string, infoBytes: Uint8Array) -> {id: number}
//	storagePieceReadAt(torrentId, pieceIndex, off: number, len: number)
//	    -> {data: Uint8Array}
//	storagePieceWriteAt(torrentId, pieceIndex, off: number, data: Uint8Array)
//	    -> {n: number}
//	storagePieceMarkComplete(torrentId, pieceIndex) -> void
//	storagePieceMarkNotComplete(torrentId, pieceIndex) -> void
//	storagePieceCompletion(torrentId, pieceIndex)
//	    -> {ok: boolean, complete: boolean}
//	storageTorrentClose(torrentId) -> void
func NewStorage() storage.ClientImpl {
	return &Storage{}
}

func (s *Storage) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	// Send the raw metainfo "info" bytes so the host can decide where to
	// stash data. Best-effort re-encode by walking metainfo.Info - but for
	// the bridge we just need the hex hash and a couple of properties; the
	// host can re-decode the spec if needed.
	v, err := callPromise(
		"storageOpen",
		infoHash.HexString(),
		info.Name,
		info.PieceLength,
		len(info.Pieces)/20,
	)
	if err != nil {
		return storage.TorrentImpl{}, fmt.Errorf("jsbridge: storageOpen: %w", err)
	}
	id := v.Get("id").Int()
	t := &jsTorrent{id: id, info: info}
	return storage.TorrentImpl{
		PieceWithHash: t.pieceWithHash,
		Close:         t.close,
	}, nil
}

type jsTorrent struct {
	id   int
	info *metainfo.Info
}

func (t *jsTorrent) close() error {
	_, err := callPromise("storageTorrentClose", t.id)
	return err
}

func (t *jsTorrent) pieceWithHash(p metainfo.Piece, _ g.Option[[]byte]) storage.PieceImpl {
	return &jsPiece{torrent: t, index: p.Index(), length: p.Length()}
}

type jsPiece struct {
	torrent *jsTorrent
	index   int
	length  int64
}

var _ storage.PieceImpl = (*jsPiece)(nil)

func (p *jsPiece) ReadAt(b []byte, off int64) (int, error) {
	if off >= p.length {
		return 0, io.EOF
	}
	if int64(len(b)) > p.length-off {
		b = b[:p.length-off]
	}
	v, err := callPromise("storagePieceReadAt", p.torrent.id, p.index, off, len(b))
	if err != nil {
		return 0, err
	}
	data := v.Get("data")
	n := data.Get("byteLength").Int()
	if n > len(b) {
		n = len(b)
	}
	if n == 0 {
		return 0, io.EOF
	}
	js.CopyBytesToGo(b[:n], data)
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *jsPiece) WriteAt(b []byte, off int64) (int, error) {
	v, err := callPromise("storagePieceWriteAt", p.torrent.id, p.index, off, bytesToJS(b))
	if err != nil {
		return 0, err
	}
	return v.Get("n").Int(), nil
}

func (p *jsPiece) MarkComplete() error {
	_, err := callPromise("storagePieceMarkComplete", p.torrent.id, p.index)
	return err
}

func (p *jsPiece) MarkNotComplete() error {
	_, err := callPromise("storagePieceMarkNotComplete", p.torrent.id, p.index)
	return err
}

func (p *jsPiece) Completion() storage.Completion {
	v, err := callPromise("storagePieceCompletion", p.torrent.id, p.index)
	if err != nil {
		return storage.Completion{Err: err}
	}
	return storage.Completion{
		Ok:       v.Get("ok").Bool(),
		Complete: v.Get("complete").Bool(),
	}
}
