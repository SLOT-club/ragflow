// Package blob implements content-defined chunking (CDC) and a local chunk
// store. It is the foundation of "Stremio-style" weight distribution: a model
// file is split into content-addressed chunks (each identified by its SHA-256),
// described by a manifest that is itself content-addressed, so peers can fetch
// and verify individual chunks on demand instead of downloading the whole file.
//
// Chunk boundaries are chosen by a GearHash rolling hash (FastCDC-style
// normalized chunking), not at fixed offsets. This is what makes weights
// deduplicable across checkpoints and quantizations: inserting or editing a few
// bytes only reshapes the chunks around the change, leaving every other chunk
// byte-identical (and therefore shared), whereas fixed-size chunking would
// shift every following boundary and destroy the dedup.
package blob

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// gear is the GearHash substitution table: one random 64-bit value per byte.
// It is filled deterministically (splitmix64 from a fixed seed) so every node,
// on every platform and build, chunks identical content identically.
var gear [256]uint64

func init() {
	x := uint64(0x1234567890abcdef)
	for i := range gear {
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z = z ^ (z >> 31)
		gear[i] = z
	}
}

// CDCConfig bounds chunk sizes and sets the target average.
type CDCConfig struct {
	Min int // no chunk smaller than this (except the file's last)
	Avg int // target average chunk size (a power of two works best)
	Max int // no chunk larger than this
}

// DefaultCDC is the chunking used for model weights: ~1 MiB average.
var DefaultCDC = CDCConfig{Min: 256 << 10, Avg: 1 << 20, Max: 4 << 20}

func log2floor(x int) uint {
	var b uint
	for x > 1 {
		x >>= 1
		b++
	}
	return b
}

func maskBits(k uint) uint64 {
	if k >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << k) - 1
}

// nextCut returns the length of the next chunk at the front of data. It uses a
// stricter mask below the average size and a looser one above it (FastCDC
// normalized chunking), which keeps chunk sizes close to Avg while remaining
// content-defined. Boundaries depend only on the leading bytes, so identical
// content always cuts identically.
func (c CDCConfig) nextCut(data []byte) int {
	n := len(data)
	if n <= c.Min {
		return n
	}
	if n > c.Max {
		n = c.Max
	}
	bits := log2floor(c.Avg)
	maskS := maskBits(bits + 2) // harder to cut before the average → fewer tiny chunks
	maskL := maskBits(bits - 2) // easier to cut after the average → fewer huge chunks
	normal := c.Avg
	if normal > n {
		normal = n
	}

	var h uint64
	i := c.Min
	for ; i < normal; i++ {
		h = (h << 1) + gear[data[i]]
		if h&maskS == 0 {
			return i
		}
	}
	for ; i < n; i++ {
		h = (h << 1) + gear[data[i]]
		if h&maskL == 0 {
			return i
		}
	}
	return n
}

// splitCDC returns the chunk lengths for an in-memory slice. ShareFile streams
// instead (it must not load a whole model into RAM); this helper backs tests
// and small inputs.
func splitCDC(data []byte, cfg CDCConfig) []int {
	var out []int
	for len(data) > 0 {
		k := cfg.nextCut(data)
		out = append(out, k)
		data = data[k:]
	}
	return out
}

// Chunk is one content-addressed piece of a file.
type Chunk struct {
	Hash string `json:"h"`
	Size int    `json:"s"`
}

// Manifest describes a file as an ordered list of content-addressed chunks.
type Manifest struct {
	ID        string  `json:"id"`         // sha256 of the manifest's content
	Name      string  `json:"name"`       // human name (e.g. model file name)
	TotalSize int64   `json:"total_size"` // bytes
	Chunks    []Chunk `json:"chunks"`     // content-defined chunks, in order
}

func (m *Manifest) computeID() string {
	body := struct {
		Name      string  `json:"name"`
		TotalSize int64   `json:"total_size"`
		Chunks    []Chunk `json:"chunks"`
	}{m.Name, m.TotalSize, m.Chunks}
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

// ShareFile content-defines a file into chunks, registers its manifest and
// chunk locations (pointing into the file itself), and returns the manifest.
func (s *Store) ShareFile(path, name string) (*Manifest, error) {
	return s.shareFileCDC(path, name, DefaultCDC)
}

func (s *Store) shareFileCDC(path, name string, cfg CDCConfig) (*Manifest, error) {
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

	m := &Manifest{Name: name, TotalSize: info.Size()}
	locs := make(map[string]chunkLoc)
	reader := bufio.NewReaderSize(f, cfg.Max)
	buf := make([]byte, 0, cfg.Max*2)
	tmp := make([]byte, cfg.Max)
	var off int64
	eof := false

	for {
		// Keep at least a full Max window available so cuts are content-defined,
		// not truncated by a buffer refill (except at end of file).
		for len(buf) < cfg.Max && !eof {
			n, rerr := reader.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if rerr == io.EOF {
				eof = true
			} else if rerr != nil {
				return nil, rerr
			}
		}
		if len(buf) == 0 {
			break
		}
		k := cfg.nextCut(buf)
		sum := sha256.Sum256(buf[:k])
		h := hex.EncodeToString(sum[:])
		m.Chunks = append(m.Chunks, Chunk{Hash: h, Size: k})
		locs[h] = chunkLoc{path: path, off: off, length: k}
		off += int64(k)

		rem := len(buf) - k
		copy(buf, buf[k:])
		buf = buf[:rem]
		if eof && rem == 0 {
			break
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
