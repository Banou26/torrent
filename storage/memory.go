package storage

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
)

// NewMemory returns a ClientImpl that stores every torrent in RAM. Pieces are
// allocated lazily as bytes arrive, so the resident footprint scales with how
// much of the torrent has actually been written, not the declared total size.
//
// This is the default storage for browser builds where a persistent
// filesystem is unavailable. Data is lost when the Client is closed.
func NewMemory() ClientImplCloser {
	return &memoryClient{completion: NewMapPieceCompletion()}
}

// NewMemoryWithCompletion is NewMemory with a caller-supplied completion
// tracker — useful for sharing completion state between in-memory and
// persistent backends.
func NewMemoryWithCompletion(pc PieceCompletion) ClientImplCloser {
	return &memoryClient{completion: pc}
}

type memoryClient struct {
	completion PieceCompletion
}

func (m *memoryClient) Close() error {
	return m.completion.Close()
}

func (m *memoryClient) OpenTorrent(_ context.Context, info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	t := &memoryTorrent{
		client:    m,
		infoHash:  infoHash,
		pieceLen:  info.PieceLength,
		numPieces: info.NumPieces(),
		pieces:    make([]memoryPiece, info.NumPieces()),
	}
	for i := range t.pieces {
		t.pieces[i].length = info.Piece(i).Length()
		t.pieces[i].index = i
		t.pieces[i].torrent = t
	}
	return TorrentImpl{
		Piece: t.Piece,
		Close: t.Close,
	}, nil
}

type memoryTorrent struct {
	client    *memoryClient
	infoHash  metainfo.Hash
	pieceLen  int64
	numPieces int
	pieces    []memoryPiece
}

func (t *memoryTorrent) Piece(p metainfo.Piece) PieceImpl {
	return &t.pieces[p.Index()]
}

func (t *memoryTorrent) Close() error {
	for i := range t.pieces {
		t.pieces[i].mu.Lock()
		t.pieces[i].data = nil
		t.pieces[i].mu.Unlock()
	}
	return nil
}

type memoryPiece struct {
	torrent *memoryTorrent
	index   int
	length  int64

	mu   sync.RWMutex
	data []byte
}

func (p *memoryPiece) ensureSizedLocked(min int64) {
	if int64(len(p.data)) < min {
		// Allocate exactly the requested size — we don't yet know whether the
		// rest of the piece will arrive, and many pieces are partial in
		// streaming use cases.
		grown := make([]byte, min)
		copy(grown, p.data)
		p.data = grown
	}
}

func (p *memoryPiece) ReadAt(b []byte, off int64) (int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if off >= int64(len(p.data)) {
		if off >= p.length {
			return 0, io.EOF
		}
		// We don't have those bytes yet — caller is asking for a region the
		// torrent client believes should be present. Surface as EOF; callers
		// have hash-verification semantics that will retry.
		return 0, io.EOF
	}
	n := copy(b, p.data[off:])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *memoryPiece) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("memory piece: negative offset")
	}
	if off+int64(len(b)) > p.length {
		return 0, errors.New("memory piece: write past end")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureSizedLocked(off + int64(len(b)))
	n := copy(p.data[off:], b)
	return n, nil
}

func (p *memoryPiece) MarkComplete() error {
	return p.torrent.client.completion.Set(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	}, true)
}

func (p *memoryPiece) MarkNotComplete() error {
	p.mu.Lock()
	p.data = nil
	p.mu.Unlock()
	return p.torrent.client.completion.Set(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	}, false)
}

func (p *memoryPiece) Completion() Completion {
	c, err := p.torrent.client.completion.Get(metainfo.PieceKey{
		InfoHash: p.torrent.infoHash,
		Index:    p.index,
	})
	c.Err = err
	return c
}
