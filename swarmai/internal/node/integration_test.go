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

	"github.com/libp2p/go-libp2p/core/peer"
)

// fakeBackend is a deterministic model backend for tests: it reports as a
// real (non-stub) llama-server so the node advertises CanInfer, and returns a
// fixed answer so redundant peers agree.
type fakeBackend struct{ text string }

func (fakeBackend) Name() string    { return "llama-server" }
func (fakeBackend) Available() bool { return true }
func (fakeBackend) Model() string   { return "testmodel" }
func (f fakeBackend) Infer(context.Context, backend.Request) backend.Result {
	return backend.Result{Text: f.text, Model: "testmodel"}
}

func newTestNode(t *testing.T, be backend.Backend) *Node {
	t.Helper()
	n, err := New(context.Background(), Config{
		ListenPort:   0,
		Backend:      be,
		IdentityPath: filepath.Join(t.TempDir(), "id.key"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func connect(t *testing.T, a, b *Node) {
	t.Helper()
	ai := peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}
	if err := a.Host.Connect(context.Background(), ai); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// waitFor polls cond until true or the deadline, failing the test on timeout.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// gossip connects two nodes and forces a capability announce so their cards
// propagate quickly.
func gossip(t *testing.T, a, b *Node) {
	connect(t, a, b)
	time.Sleep(2 * time.Second) // let the pubsub mesh form
	a.AnnounceNow()
	b.AnnounceNow()
}

func TestGossipAndComputeRouting(t *testing.T) {
	weak := newTestNode(t, backend.Stub{}) // no model
	strong := newTestNode(t, fakeBackend{text: "42 is the answer"})
	gossip(t, weak, strong)

	// The weak node should learn the strong node can serve inference.
	waitFor(t, "weak to discover an infer peer", 20*time.Second, func() bool {
		_, _, ok := weak.reg.BestInferPeer("")
		return ok
	})

	res, servedBy, err := weak.Run(context.Background(), backend.Request{Prompt: "q"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "42 is the answer" {
		t.Fatalf("text = %q, want the strong node's answer", res.Text)
	}
	if servedBy != strong.Host.ID().String() {
		t.Fatalf("served by %q, want the strong peer %q", servedBy, strong.Host.ID())
	}

	// The weak node should have credited the peer that served it.
	waitFor(t, "credit to accrue", 3*time.Second, func() bool {
		for _, r := range weak.Credits() {
			if r.PeerID == strong.Host.ID().String() && r.Credits > 0 {
				return true
			}
		}
		return false
	})
}

func TestRedundantMajority(t *testing.T) {
	weak := newTestNode(t, backend.Stub{})
	a := newTestNode(t, fakeBackend{text: "same answer"})
	b := newTestNode(t, fakeBackend{text: "same answer"})
	gossip(t, weak, a)
	gossip(t, weak, b)

	waitFor(t, "two infer peers", 20*time.Second, func() bool {
		return len(weak.inferPeers("", 3)) >= 2
	})

	res, servers, err := weak.RunRedundant(context.Background(), backend.Request{Prompt: "q"}, 2)
	if err != nil {
		t.Fatalf("RunRedundant: %v", err)
	}
	if res.Text != "same answer" {
		t.Fatalf("majority text = %q", res.Text)
	}
	if len(servers) != 2 {
		t.Fatalf("agreeing peers = %d, want 2", len(servers))
	}
}

func TestBlobStreamingRoundtrip(t *testing.T) {
	seeder := newTestNode(t, backend.Stub{})
	fetcher := newTestNode(t, backend.Stub{})
	gossip(t, fetcher, seeder)

	// Seeder shares a model file spanning multiple chunks (2 full + a partial).
	data := make([]byte, 2*(4<<20)+123)
	_, _ = rand.Read(data)
	path := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := seeder.ShareModel(path, "model")
	if err != nil {
		t.Fatalf("ShareModel: %v", err)
	}
	seeder.AnnounceNow()

	// Fetcher learns who seeds the manifest.
	waitFor(t, "fetcher to see the seed", 20*time.Second, func() bool {
		return len(fetcher.reg.SeedersFor(m.ID)) > 0
	})

	out := filepath.Join(t.TempDir(), "out.bin")
	if _, _, err := fetcher.FetchModelAuto(context.Background(), m.ID, out, "", 4); err != nil {
		t.Fatalf("FetchModelAuto: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("streamed file differs from the original")
	}
}
