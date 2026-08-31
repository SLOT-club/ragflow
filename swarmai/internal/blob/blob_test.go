package blob

import (
	"bytes"
	"crypto/rand"
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
	// 2 full chunks plus a partial one.
	path, data := writeTemp(t, ChunkSize*2+123)
	s := NewStore()
	m, err := s.ShareFile(path, "model")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}
	if m.TotalSize != int64(len(data)) {
		t.Fatalf("TotalSize = %d, want %d", m.TotalSize, len(data))
	}
	if len(m.Chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(m.Chunks))
	}

	// Reassemble from chunks and compare byte-for-byte.
	var got []byte
	for _, h := range m.Chunks {
		b, err := s.ReadChunk(h)
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
	path, _ := writeTemp(t, ChunkSize+10)
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
	path, _ := writeTemp(t, 100)
	s := NewStore()
	m, err := s.ShareFile(path, "model")
	if err != nil {
		t.Fatal(err)
	}
	seeds := s.Seeds()
	if len(seeds) != 1 || seeds[0].ID != m.ID {
		t.Fatalf("Seeds = %+v, want one with id %s", seeds, m.ID)
	}
	if !s.HasChunk(m.Chunks[0]) {
		t.Fatal("HasChunk false for a seeded chunk")
	}
	if _, err := s.ReadChunk("00deadbeef"); err == nil {
		t.Fatal("ReadChunk of unknown chunk should error")
	}
}
