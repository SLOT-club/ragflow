package blob

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand: %v", err)
	}
	path := filepath.Join(t.TempDir(), "m.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, data
}

func TestShareChunkRoundtrip(t *testing.T) {
	path, data := writeTemp(t, 5<<20) // 5 MiB → several content-defined chunks
	s := NewStore()
	m, err := s.ShareFile(path, "model")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}
	if m.TotalSize != int64(len(data)) {
		t.Fatalf("TotalSize = %d, want %d", m.TotalSize, len(data))
	}
	if len(m.Chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(m.Chunks))
	}

	// Sizes respect the bounds (every chunk but the last is >= Min; all <= Max).
	var total int64
	for i, ch := range m.Chunks {
		if ch.Size > DefaultCDC.Max {
			t.Fatalf("chunk %d size %d exceeds Max %d", i, ch.Size, DefaultCDC.Max)
		}
		if i < len(m.Chunks)-1 && ch.Size < DefaultCDC.Min {
			t.Fatalf("non-final chunk %d size %d below Min %d", i, ch.Size, DefaultCDC.Min)
		}
		total += int64(ch.Size)
	}
	if total != m.TotalSize {
		t.Fatalf("chunk sizes sum to %d, want %d", total, m.TotalSize)
	}

	// Reassemble from chunks and compare byte-for-byte.
	var got []byte
	for _, ch := range m.Chunks {
		b, err := s.ReadChunk(ch.Hash)
		if err != nil {
			t.Fatalf("ReadChunk: %v", err)
		}
		got = append(got, b...)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled bytes differ from original")
	}
}

func TestManifestDeterministic(t *testing.T) {
	path, _ := writeTemp(t, 2<<20)
	m1, err := NewStore().ShareFile(path, "model")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := NewStore().ShareFile(path, "model")
	if err != nil {
		t.Fatal(err)
	}
	if m1.ID != m2.ID {
		t.Fatalf("manifest id not deterministic: %s != %s", m1.ID, m2.ID)
	}
}

func TestSeedsAndUnknownChunk(t *testing.T) {
	path, _ := writeTemp(t, 300<<10)
	s := NewStore()
	m, err := s.ShareFile(path, "model")
	if err != nil {
		t.Fatal(err)
	}
	seeds := s.Seeds()
	if len(seeds) != 1 || seeds[0].ID != m.ID {
		t.Fatalf("Seeds = %+v, want one with id %s", seeds, m.ID)
	}
	if !s.HasChunk(m.Chunks[0].Hash) {
		t.Fatal("HasChunk false for a seeded chunk")
	}
	if _, err := s.ReadChunk("00deadbeef"); err == nil {
		t.Fatal("ReadChunk of unknown chunk should error")
	}
}

// hashRegions hashes consecutive regions of data given their lengths, returning
// the set of chunk hashes.
func hashRegions(data []byte, lens []int) map[string]bool {
	set := make(map[string]bool)
	off := 0
	for _, l := range lens {
		sum := sha256.Sum256(data[off : off+l])
		set[hex.EncodeToString(sum[:])] = true
		off += l
	}
	return set
}

func sharedFraction(aData []byte, aLens []int, bData []byte, bLens []int) float64 {
	a := hashRegions(aData, aLens)
	b := hashRegions(bData, bLens)
	shared := 0
	for h := range a {
		if b[h] {
			shared++
		}
	}
	return float64(shared) / float64(len(a))
}

func fixedLens(n, size int) []int {
	var out []int
	for n > 0 {
		l := size
		if l > n {
			l = n
		}
		out = append(out, l)
		n -= l
	}
	return out
}

// TestCDCDedupSurvivesInsertion is the point of content-defined chunking:
// after inserting bytes near the start of a file, CDC keeps almost all chunks
// identical (they re-synchronize after the edit), while fixed-size chunking
// shifts every following boundary and shares almost nothing.
func TestCDCDedupSurvivesInsertion(t *testing.T) {
	cfg := CDCConfig{Min: 2 << 10, Avg: 8 << 10, Max: 32 << 10}

	data := make([]byte, 1<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	// Insert 50 fresh bytes 16 KiB in — early, so fixed-size chunking suffers most.
	p := 16 << 10
	extra := make([]byte, 50)
	_, _ = rand.Read(extra)
	variant := append(append(append([]byte{}, data[:p]...), extra...), data[p:]...)

	cdcShared := sharedFraction(data, splitCDC(data, cfg), variant, splitCDC(variant, cfg))

	fixed := cfg.Avg
	fixShared := sharedFraction(data, fixedLens(len(data), fixed), variant, fixedLens(len(variant), fixed))

	t.Logf("shared chunks after insertion: CDC=%.2f  fixed-size=%.2f", cdcShared, fixShared)
	if cdcShared < 0.5 {
		t.Fatalf("CDC dedup too low: %.2f (expected most chunks to survive)", cdcShared)
	}
	if cdcShared <= fixShared {
		t.Fatalf("CDC (%.2f) should beat fixed-size (%.2f) under insertion", cdcShared, fixShared)
	}
}
