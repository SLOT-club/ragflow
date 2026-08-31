// Package blob implements content-addressed chunking and a local chunk store.
// It is the foundation of "Stremio-style" weight distribution: a model file is
// split into fixed-size chunks each identified by its SHA-256, described by a
// manifest that is itself content-addressed. Peers can then fetch and verify
// individual chunks on demand instead of downloading the whole file.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ChunkSize is the fixed chunk size (4 MiB). Small enough for on-demand fetch,
// large enough to keep per-chunk overhead low.
const ChunkSize = 4 << 20

// Manifest describes a file as an ordered list of content-addressed chunks.
type Manifest struct {
	ID        string   `json:"id"`         // sha256 of the manifest's content
	Name      string   `json:"name"`       // human name (e.g. model file name)
	TotalSize int64    `json:"total_size"` // bytes
	ChunkSize int      `json:"chunk_size"` // bytes per chunk (last may be smaller)
	Chunks    []string `json:"chunks"`     // hex sha256 of each chunk, in order
}

// computeID derives the manifest ID from its content (excluding the ID field).
func (m *Manifest) computeID() string {
	body := struct {
		Name      string   `json:"name"`
		TotalSize int64    `json:"total_size"`
		ChunkSize int      `json:"chunk_size"`
		Chunks    []string `json:"chunks"`
	}{m.Name, m.TotalSize, m.ChunkSize, m.Chunks}
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SeedInfo is the compact advertisement of a manifest a node is seeding.
type SeedInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SizeMB int64  `json:"size_mb"`
	Chunks int    `json:"chunks"`
}

// chunkLoc says where a chunk's bytes live: a slice of some file on disk.
type chunkLoc struct {
	path   string
	off    int64
	length int
}

// Store is a node's content-addressed store. It never duplicates large files:
// a shared file's chunks are served by reading slices of the original, and
// received chunks are recorded pointing into the destination file.
type Store struct {
	mu        sync.RWMutex
	manifests map[string]*Manifest
	chunks    map[string]chunkLoc
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{manifests: make(map[string]*Manifest), chunks: make(map[string]chunkLoc)}
}

// ShareFile chunks a file, registers its manifest and chunk locations (pointing
// into the file itself), and returns the manifest so it can be advertised.
func (s *Store) ShareFile(path, name string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = filepath.Base(path)
	}

	m := &Manifest{Name: name, TotalSize: info.Size(), ChunkSize: ChunkSize}
	locs := make(map[string]chunkLoc)
	buf := make([]byte, ChunkSize)
	var off int64
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			sum := sha256.Sum256(buf[:n])
			h := hex.EncodeToString(sum[:])
			m.Chunks = append(m.Chunks, h)
			locs[h] = chunkLoc{path: path, off: off, length: n}
			off += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	m.ID = m.computeID()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[m.ID] = m
	for h, l := range locs {
		s.chunks[h] = l
	}
	return m, nil
}

// Manifest returns a stored manifest by ID.
func (s *Store) Manifest(id string) (*Manifest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.manifests[id]
	return m, ok
}

// AddManifest records a manifest fetched from a peer.
func (s *Store) AddManifest(m *Manifest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manifests[m.ID] = m
}

// ReadChunk returns the bytes of a chunk this node holds.
func (s *Store) ReadChunk(hash string) ([]byte, error) {
	s.mu.RLock()
	loc, ok := s.chunks[hash]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("chunk %s not held", short(hash))
	}
	f, err := os.Open(loc.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, loc.length)
	if _, err := f.ReadAt(b, loc.off); err != nil {
		return nil, err
	}
	return b, nil
}

// RegisterChunk records that this node now holds a chunk at a location, so it
// can re-seed it to others (turning every downloader into a seeder).
func (s *Store) RegisterChunk(hash, path string, off int64, length int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks[hash] = chunkLoc{path: path, off: off, length: length}
}

// HasChunk reports whether this node can serve a chunk.
func (s *Store) HasChunk(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.chunks[hash]
	return ok
}

// Seeds lists the manifests this node is seeding.
func (s *Store) Seeds() []SeedInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SeedInfo, 0, len(s.manifests))
	for _, m := range s.manifests {
		out = append(out, SeedInfo{ID: m.ID, Name: m.Name, SizeMB: m.TotalSize >> 20, Chunks: len(m.Chunks)})
	}
	return out
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
