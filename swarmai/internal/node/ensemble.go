package node

import (
	"context"
	"sort"
	"strings"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/route"
	"github.com/slot-club/swarmai/internal/verify"

	"github.com/libp2p/go-libp2p/core/peer"
)

// EnsembleResult is the outcome of a combo (routed + cross-checked) answer.
type EnsembleResult struct {
	Answer      string `json:"answer"`
	Domain      string `json:"domain"`
	Difficulty  string `json:"difficulty"`
	Primary     string `json:"primary"`               // model@peer that answered
	Verifier    string `json:"verifier,omitempty"`    // model@peer that cross-checked
	Adjudicator string `json:"adjudicator,omitempty"` // model@peer that broke a tie
	Agreed      bool   `json:"agreed"`                // primary and verifier agreed
	Confidence  string `json:"confidence"`            // unchecked|verified|disputed|adjudicated
}

type cand struct {
	id   peer.ID
	card CapabilityCard
}

// candidates returns every infer-capable node (peers plus self).
func (n *Node) candidates() []cand {
	var out []cand
	for id, c := range n.reg.Snapshot() {
		if c.CanInfer {
			out = append(out, cand{id, c})
		}
	}
	if self := n.SelfCard(); self.CanInfer {
		out = append(out, cand{n.Host.ID(), self})
	}
	return out
}

// rankForPrimary orders candidates as the primary answerer for a domain and
// difficulty: domain-tag match first, then the tier that fits the difficulty
// (high tier for hard questions, low tier for simple ones — cheaper is better
// when it suffices), then reputation credits, then a stable model-name key.
func (n *Node) rankForPrimary(cands []cand, domain, difficulty string) []cand {
	out := append([]cand(nil), cands...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if am, bm := a.card.hasTag(domain), b.card.hasTag(domain); am != bm {
			return am
		}
		ar, br := route.TierRank(a.card.Tier), route.TierRank(b.card.Tier)
		if difficulty == route.Simple {
			ar, br = -ar, -br // simple question → prefer the cheaper (lower) tier
		}
		if ar != br {
			return ar > br
		}
		ac, bc := n.credits.Get(a.id).Credits, n.credits.Get(b.id).Credits
		if ac != bc {
			return ac > bc
		}
		return a.card.Model < b.card.Model
	})
	return out
}

// pickDifferent returns the first candidate whose model differs from all of
// `exclude`, ordered by tier: ascending (cheapest first) when preferCheap, else
// descending (strongest first). This gives a cheap cross-checker or a strong
// adjudicator.
func (n *Node) pickDifferent(cands []cand, preferCheap bool, exclude ...string) (cand, bool) {
	excl := map[string]bool{}
	for _, m := range exclude {
		excl[m] = true
	}
	pool := make([]cand, 0, len(cands))
	for _, c := range cands {
		if !excl[c.card.Model] {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return cand{}, false
	}
	sort.Slice(pool, func(i, j int) bool {
		ar, br := route.TierRank(pool[i].card.Tier), route.TierRank(pool[j].card.Tier)
		if ar != br {
			if preferCheap {
				return ar < br
			}
			return ar > br
		}
		return pool[i].card.Model < pool[j].card.Model
	})
	return pool[0], true
}

func (n *Node) label(id peer.ID, model string) string {
	short := id.String()
	if len(short) > 10 {
		short = short[len(short)-6:]
	}
	if model == "" {
		model = "?"
	}
	return model + "@" + short
}

// answerOn runs a request on a specific node (local backend if it is us,
// otherwise a remote infer call), crediting a remote peer that serves.
func (n *Node) answerOn(ctx context.Context, id peer.ID, req backend.Request) backend.Result {
	if id == n.Host.ID() {
		return n.backend.Infer(ctx, req)
	}
	res, err := n.requestRemote(ctx, id, req)
	if err != nil {
		return backend.Result{Err: err.Error()}
	}
	n.credits.Earn(id, 1)
	return res
}

// verifyOn asks a specific node to verify a draft (local or remote).
func (n *Node) verifyOn(ctx context.Context, id peer.ID, prompt, draft string) verify.Verdict {
	if id == n.Host.ID() {
		out := n.backend.Infer(ctx, backend.Request{Prompt: verify.BuildVerifyPrompt(prompt, draft), MaxTokens: 512})
		if out.Err != "" {
			return verify.Verdict{Err: out.Err}
		}
		return verify.Interpret(draft, out.Text)
	}
	v, err := n.requestVerify(ctx, id, verify.Request{Prompt: prompt, Draft: draft, MaxTokens: 512})
	if err != nil {
		return verify.Verdict{Err: err.Error()}
	}
	return v
}

func norm(s string) string { return strings.TrimSpace(strings.ToLower(s)) }

// RunEnsemble answers a prompt as a "combo": it classifies the prompt, routes it
// to the best model for that domain and difficulty, has a different (typically
// cheaper) model cross-check the answer, and — only if they disagree — brings in
// a strong third model to adjudicate. Agreement short-circuits, so the common
// case reaches high confidence without redoing the work.
func (n *Node) RunEnsemble(ctx context.Context, prompt string) EnsembleResult {
	diff, domain := route.Classify(prompt)
	res := EnsembleResult{Domain: domain, Difficulty: diff, Confidence: "unchecked"}
	req := backend.Request{Prompt: prompt, MaxTokens: 512}

	cands := n.candidates()
	if len(cands) == 0 {
		out := n.backend.Infer(ctx, req)
		res.Answer = out.Text
		res.Primary = n.label(n.Host.ID(), n.backend.Model()) + " (local)"
		return res
	}

	ranked := n.rankForPrimary(cands, domain, diff)
	primary := ranked[0]
	ans := n.answerOn(ctx, primary.id, req)
	res.Primary = n.label(primary.id, primary.card.Model)
	res.Answer = ans.Text

	// Cross-check with a different, cheaper model.
	verifier, ok := n.pickDifferent(cands, true, primary.card.Model)
	if !ok {
		return res // only one model available: unchecked
	}
	res.Verifier = n.label(verifier.id, verifier.card.Model)
	verdict := n.verifyOn(ctx, verifier.id, prompt, ans.Text)
	if verdict.Err == "" && verdict.Accepted {
		n.credits.Earn(primary.id, 1)
		n.credits.Earn(verifier.id, 1)
		res.Answer = verdict.Answer
		res.Agreed = true
		res.Confidence = "verified"
		return res
	}

	// Disagreement: adjudicate with the strongest different model.
	adj, ok := n.pickDifferent(cands, false, primary.card.Model, verifier.card.Model)
	if !ok {
		res.Confidence = "disputed" // no third model to settle it
		return res
	}
	res.Adjudicator = n.label(adj.id, adj.card.Model)
	adjAns := n.answerOn(ctx, adj.id, req)

	primAgree := norm(ans.Text) == norm(adjAns.Text)
	verAgree := verdict.Err == "" && norm(verdict.Answer) == norm(adjAns.Text)
	n.credits.Agreement(primary.id, primAgree)
	n.credits.Agreement(verifier.id, verAgree)
	n.credits.Earn(adj.id, 1)

	switch {
	case primAgree:
		res.Answer = ans.Text
	case verAgree:
		res.Answer = verdict.Answer
	default:
		res.Answer = adjAns.Text // neither matched: trust the adjudicator's own answer
	}
	res.Confidence = "adjudicated"
	return res
}
