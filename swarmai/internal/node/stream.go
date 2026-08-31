package node

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/slot-club/swarmai/internal/blob"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// BlobProtocol streams content-addressed model chunks between peers. This is the
// "Stremio-style" transport: fetch and verify the pieces you need, on demand.
const BlobProtocol protocol.ID = "/swarmai/blob/1.0.0"

type blobRequest struct {
	Type string `json:"type"` // "manifest" | "chunk"
	ID   string `json:"id"`   // manifest id or chunk hash
}

// handleBlobStream serves a manifest or a single chunk. Wire format of the
// response: one status byte (1=ok, 0=not found); then, for a manifest, the JSON
// manifest; for a chunk, an 8-byte big-endian length followed by the bytes.
func (n *Node) handleBlobStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(5 * time.Minute))

	var req blobRequest
	if err := json.NewDecoder(s).Decode(&req); err != nil {
		return
	}
	switch req.Type {
	case "manifest":
		m, ok := n.blobs.Manifest(req.ID)
		if !ok {
			_, _ = s.Write([]byte{0})
			return
		}
		_, _ = s.Write([]byte{1})
		_ = json.NewEncoder(s).Encode(m)
	case "chunk":
		b, err := n.blobs.ReadChunk(req.ID)
		if err != nil {
			_, _ = s.Write([]byte{0})
			return
		}
		_, _ = s.Write([]byte{1})
		var lb [8]byte
		binary.BigEndian.PutUint64(lb[:], uint64(len(b)))
		_, _ = s.Write(lb[:])
		_, _ = s.Write(b)
	default:
		_, _ = s.Write([]byte{0})
	}
}

// fetchManifest requests a manifest from a peer.
func (n *Node) fetchManifest(ctx context.Context, target peer.ID, id string) (*blob.Manifest, error) {
	s, err := n.Host.NewStream(ctx, target, BlobProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(60 * time.Second))
	if err := json.NewEncoder(s).Encode(blobRequest{Type: "manifest", ID: id}); err != nil {
		return nil, err
	}
	_ = s.CloseWrite()

	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return nil, err
	}
	if status[0] != 1 {
		return nil, fmt.Errorf("peer does not have manifest %s", id)
	}
	var m blob.Manifest
	if err := json.NewDecoder(s).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// fetchChunk requests one chunk from a peer and verifies its hash.
func (n *Node) fetchChunk(ctx context.Context, target peer.ID, hash string) ([]byte, error) {
	s, err := n.Host.NewStream(ctx, target, BlobProtocol)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(2 * time.Minute))
	if err := json.NewEncoder(s).Encode(blobRequest{Type: "chunk", ID: hash}); err != nil {
		return nil, err
	}
	_ = s.CloseWrite()

	status := make([]byte, 1)
	if _, err := io.ReadFull(s, status); err != nil {
		return nil, err
	}
	if status[0] != 1 {
		return nil, fmt.Errorf("peer does not have chunk %s", hash)
	}
	var lb [8]byte
	if _, err := io.ReadFull(s, lb[:]); err != nil {
		return nil, err
	}
	n2 := binary.BigEndian.Uint64(lb[:])
	b := make([]byte, n2)
	if _, err := io.ReadFull(s, b); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("chunk hash mismatch")
	}
	return b, nil
}

// FetchModel streams a whole model file from a seeding peer: it pulls the
// manifest, then fetches every chunk (verifying each) with a bounded prefetch
// window, assembling them into outPath and re-registering them so this node
// becomes a seeder too. Returns the manifest on success.
//
// window is the number of chunks fetched concurrently — the "Stremio buffer".
func (n *Node) FetchModel(ctx context.Context, seeder peer.ID, manifestID, outPath string, window int) (*blob.Manifest, error) {
	if window <= 0 {
		window = 8
	}
	m, err := n.fetchManifest(ctx, seeder, manifestID)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	n.blobs.AddManifest(m)

	out, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	if err := out.Truncate(m.TotalSize); err != nil {
		return nil, err
	}

	sem := make(chan struct{}, window)
	errCh := make(chan error, len(m.Chunks))
	var wg sync.WaitGroup
	for i, h := range m.Chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, hash string) {
			defer wg.Done()
			defer func() { <-sem }()
			b, err := n.fetchChunk(ctx, seeder, hash)
			if err != nil {
				errCh <- fmt.Errorf("chunk %d: %w", idx, err)
				return
			}
			off := int64(idx) * int64(m.ChunkSize)
			if _, err := out.WriteAt(b, off); err != nil {
				errCh <- err
				return
			}
			n.blobs.RegisterChunk(hash, outPath, off, len(b))
		}(i, h)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			return nil, e
		}
	}
	return m, nil
}

// ShareModel chunks and registers a local file for seeding, returning its manifest.
func (n *Node) ShareModel(path, name string) (*blob.Manifest, error) {
	return n.blobs.ShareFile(path, name)
}

// LocalSeeds lists the manifests this node seeds.
func (n *Node) LocalSeeds() []blob.SeedInfo { return n.blobs.Seeds() }
