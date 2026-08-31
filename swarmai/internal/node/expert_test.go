package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/blob"
)

// TestFetchPartOnDemand proves the "huge model, tiny working set" property: a
// fetcher pulls only the chunks covering one named part (an expert), not the
// whole model, and the hot cache holds far less than the full file.
func TestFetchPartOnDemand(t *testing.T) {
	seeder := newTestNode(t, backend.Stub{})
	fetcher := newTestNode(t, backend.Stub{})
	gossip(t, fetcher, seeder)

	data := make([]byte, 5<<20) // 5 MiB model
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	const expOff, expLen = int64(3<<20) + 12345, int64(200 << 10)
	parts := map[string]blob.Range{
		"head":     {Offset: 0, Length: 100 << 10},
		"expert.7": {Offset: expOff, Length: expLen},
	}
	m, err := seeder.ShareModelWithParts(path, "model", parts)
	if err != nil {
		t.Fatalf("ShareModelWithParts: %v", err)
	}
	seeder.AnnounceNow()

	waitFor(t, "fetcher to see the seed", 20*1e9, func() bool {
		return len(fetcher.reg.SeedersFor(m.ID)) > 0
	})

	// Fetch only expert.7.
	got, _, err := fetcher.FetchPartAuto(context.Background(), m.ID, "expert.7", "")
	if err != nil {
		t.Fatalf("FetchPartAuto: %v", err)
	}
	want := data[expOff : expOff+expLen]
	if !bytes.Equal(got, want) {
		t.Fatal("fetched part bytes differ from the original range")
	}

	// The cache holds only the covering chunks — far less than the whole model.
	used, _, count := fetcher.ExpertCacheStats()
	if used == 0 || count == 0 {
		t.Fatal("cache should hold the fetched chunks")
	}
	if used >= m.TotalSize {
		t.Fatalf("cache used %d should be far below the model size %d", used, m.TotalSize)
	}

	// A second fetch of the same part is served from cache and still correct.
	got2, _, err := fetcher.FetchPartAuto(context.Background(), m.ID, "expert.7", "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, want) {
		t.Fatal("second (cached) fetch differs")
	}
}
