package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/verify"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// VerifyProtocol is the draft→verify (M2) stream protocol.
const VerifyProtocol protocol.ID = verify.Protocol

// handleVerifyStream runs the local (stronger) model as a verifier: it checks a
// peer's draft in one pass and returns accept-or-correct.
func (n *Node) handleVerifyStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Minute))

	var req verify.Request
	if err := json.NewDecoder(s).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(verify.Verdict{Err: fmt.Sprintf("decode: %v", err)})
		return
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 512
	}
	out := n.backend.Infer(context.Background(), backend.Request{
		Prompt:    verify.BuildVerifyPrompt(req.Prompt, req.Draft),
		Model:     req.Model,
		MaxTokens: maxTok,
	})
	var v verify.Verdict
	if out.Err != "" {
		v = verify.Verdict{Err: out.Err}
	} else {
		v = verify.Interpret(req.Draft, out.Text)
	}
	v.VerifiedBy = n.Host.ID().String()
	_ = json.NewEncoder(s).Encode(v)
}

// requestVerify sends a draft to a verifier peer and returns its verdict.
func (n *Node) requestVerify(ctx context.Context, target peer.ID, req verify.Request) (verify.Verdict, error) {
	s, err := n.Host.NewStream(ctx, target, VerifyProtocol)
	if err != nil {
		return verify.Verdict{}, fmt.Errorf("open verify stream: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Minute))
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return verify.Verdict{}, err
	}
	if err := s.CloseWrite(); err != nil {
		return verify.Verdict{}, err
	}
	var v verify.Verdict
	if err := json.NewDecoder(s).Decode(&v); err != nil {
		return verify.Verdict{}, err
	}
	return v, nil
}

// RunSpeculative answers a prompt with the draft→verify pattern: draft locally
// with this node's (small) model, then have a stronger peer verify or correct
// it in one round-trip. Falls back to the local draft if no verifier peer
// exists. Returns the verdict and the peer that verified.
func (n *Node) RunSpeculative(ctx context.Context, prompt, model string) (verify.Verdict, string, error) {
	draft := n.backend.Infer(ctx, backend.Request{Prompt: prompt, MaxTokens: 256})
	if draft.Err != "" {
		return verify.Verdict{Err: draft.Err}, "", nil
	}

	target, _, ok := n.reg.BestInferPeer(model)
	if !ok || target == n.Host.ID() {
		return verify.Verdict{
			Accepted: true,
			Answer:   draft.Text,
			Note:     "no verifier peer; local draft only",
		}, n.Host.ID().String() + " (local)", nil
	}

	v, err := n.requestVerify(ctx, target, verify.Request{
		Prompt: prompt, Draft: draft.Text, Model: model, MaxTokens: 512,
	})
	if err != nil {
		// Fall back to the local draft rather than failing the request.
		return verify.Verdict{Accepted: true, Answer: draft.Text, Note: "verifier unreachable; local draft"}, target.String(), err
	}
	n.credits.Earn(target, 1) // reward the verifier for useful work
	return v, target.String(), nil
}
