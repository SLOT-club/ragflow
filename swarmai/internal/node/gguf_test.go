package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/gguf"
)

// TestShareGGUFThenFetchExpert proves the full pipeline: sharing a real GGUF
// file auto-derives a part→range map from its tensors, and a peer can then
// fetch one named expert by tensor name and get exactly that tensor's bytes.
func TestShareGGUFThenFetchExpert(t *testing.T) {
	seeder := newTestNode(t, backend.Stub{})
	fetcher := newTestNode(t, backend.Stub{})
	gossip(t, fetcher, seeder)

	// Build a GGUF file with a named expert tensor.
	path := filepath.Join(t.TempDir(), "model.gguf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tensors := []gguf.TensorInfo{
		{Name: "blk.0.attn", Offset: 0, Dims: []uint64{64}, Type: 0},
		{Name: "blk.0.ffn.experts.3", Offset: 256, Dims: []uint64{64}, Type: 0},
		{Name: "blk.0.ffn.experts.7", Offset: 512, Dims: []uint64{64}, Type: 0},
	}
	dataStart, err := gguf.WriteHeader(f, tensors, 32)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 768)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m, err := seeder.ShareModel(path, "model.gguf")
	if err != nil {
		t.Fatalf("ShareModel: %v", err)
	}

	// The manifest should have gained the tensor layout automatically.
	sm, ok := seeder.blobs.Manifest(m.ID)
	if !ok || sm.Parts["blk.0.ffn.experts.3"].Length == 0 {
		t.Fatalf("GGUF parts not attached: %+v", sm.Parts)
	}
	seeder.AnnounceNow()

	waitFor(t, "fetcher to see the seed", 20*time.Second, func() bool {
		return len(fetcher.reg.SeedersFor(m.ID)) > 0
	})

	got, _, err := fetcher.FetchPartAuto(context.Background(), m.ID, "blk.0.ffn.experts.3", "")
	if err != nil {
		t.Fatalf("FetchPartAuto: %v", err)
	}

	// Expected bytes are the tensor's range within the file (offset 256, len 256).
	want := data[256 : 256+256]
	if !bytes.Equal(got, want) {
		t.Fatalf("fetched expert bytes differ from the tensor's data (dataStart=%d)", dataStart)
	}
}
