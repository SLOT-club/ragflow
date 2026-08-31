package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
)

// progBackend is a programmable model for tests: it returns a fixed answer to
// normal prompts, and either ACCEPT or a fixed correction to verify prompts.
type progBackend struct {
	model      string
	answer     string
	accept     bool
	correction string
}

func (progBackend) Name() string    { return "llama-server" }
func (progBackend) Available() bool { return true }
func (b progBackend) Model() string { return b.model }
func (b progBackend) Infer(_ context.Context, req backend.Request) backend.Result {
	if strings.Contains(req.Prompt, "You verify a draft answer") {
		if b.accept {
			return backend.Result{Text: "ACCEPT", Model: b.model}
		}
		return backend.Result{Text: b.correction, Model: b.model}
	}
	return backend.Result{Text: b.answer, Model: b.model}
}

func newTestNodeFull(t *testing.T, be backend.Backend, tier string, tags []string) *Node {
	t.Helper()
	n, err := New(context.Background(), Config{
		ListenPort:   0,
		Backend:      be,
		IdentityPath: filepath.Join(t.TempDir(), "id.key"),
		Tier:         tier,
		Tags:         tags,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// TestEnsembleAgreement: primary answers, a cheaper different model verifies and
// accepts → high confidence with no extra work.
func TestEnsembleAgreement(t *testing.T) {
	asker := newTestNode(t, backend.Stub{}) // pure orchestrator
	big := newTestNodeFull(t, progBackend{model: "big", answer: "42"}, "large", nil)
	small := newTestNodeFull(t, progBackend{model: "small", accept: true}, "small", nil)
	gossip(t, asker, big)
	gossip(t, asker, small)

	waitFor(t, "two models", 20*time.Second, func() bool {
		return len(asker.candidates()) >= 2
	})

	res := asker.RunEnsemble(context.Background(), "Explain step by step why 6*7 equals what")
	if res.Answer != "42" {
		t.Fatalf("answer = %q, want 42", res.Answer)
	}
	if !res.Agreed || res.Confidence != "verified" {
		t.Fatalf("expected agreed/verified, got agreed=%v conf=%s", res.Agreed, res.Confidence)
	}
	if !strings.HasPrefix(res.Primary, "big@") || !strings.HasPrefix(res.Verifier, "small@") {
		t.Fatalf("routing: primary=%s verifier=%s", res.Primary, res.Verifier)
	}
}

// TestEnsembleAdjudication: the cheap verifier disagrees, so a strong third
// model adjudicates; the adjudicator agrees with the primary, which wins.
func TestEnsembleAdjudication(t *testing.T) {
	asker := newTestNode(t, backend.Stub{})
	big := newTestNodeFull(t, progBackend{model: "big", answer: "42"}, "large", nil)
	small := newTestNodeFull(t, progBackend{model: "small", accept: false, correction: "43"}, "small", nil)
	huge := newTestNodeFull(t, progBackend{model: "huge", answer: "42"}, "large", nil)
	gossip(t, asker, big)
	gossip(t, asker, small)
	gossip(t, asker, huge)

	waitFor(t, "three models", 25*time.Second, func() bool {
		return len(asker.candidates()) >= 3
	})

	res := asker.RunEnsemble(context.Background(), "Prove step by step the value of 6*7")
	if res.Confidence != "adjudicated" {
		t.Fatalf("confidence = %s, want adjudicated", res.Confidence)
	}
	if res.Adjudicator == "" {
		t.Fatal("expected an adjudicator")
	}
	if res.Answer != "42" {
		t.Fatalf("answer = %q, want 42 (adjudicator agreed with primary)", res.Answer)
	}
}
