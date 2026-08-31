package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slot-club/swarmai/internal/backend"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// InferProtocol is the libp2p stream protocol for one-shot inference requests.
const InferProtocol protocol.ID = "/swarmai/infer/1.0.0"

// wireRequest/wireResponse are the JSON messages exchanged over an infer stream.
type wireRequest struct {
	backend.Request
}

type wireResponse struct {
	backend.Result
	ServedBy string `json:"served_by"`
}

// handleInferStream serves an incoming inference request using the local
// backend. This is the "share my compute" side of the network.
func (n *Node) handleInferStream(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Minute))

	var req wireRequest
	if err := json.NewDecoder(s).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(wireResponse{
			Result:   backend.Result{Err: fmt.Sprintf("decode request: %v", err)},
			ServedBy: n.Host.ID().String(),
		})
		return
	}

	res := n.backend.Infer(context.Background(), req.Request)
	_ = json.NewEncoder(s).Encode(wireResponse{Result: res, ServedBy: n.Host.ID().String()})
}

// requestRemote sends a request to a specific peer and returns its result.
func (n *Node) requestRemote(ctx context.Context, target peer.ID, req backend.Request) (backend.Result, error) {
	s, err := n.Host.NewStream(ctx, target, InferProtocol)
	if err != nil {
		return backend.Result{}, fmt.Errorf("open stream to %s: %w", target.String(), err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Minute))

	if err := json.NewEncoder(s).Encode(wireRequest{Request: req}); err != nil {
		return backend.Result{}, fmt.Errorf("send request: %w", err)
	}
	// Signal end of request so the remote can start reading/answering.
	if err := s.CloseWrite(); err != nil {
		return backend.Result{}, fmt.Errorf("close write: %w", err)
	}

	var resp wireResponse
	if err := json.NewDecoder(s).Decode(&resp); err != nil {
		return backend.Result{}, fmt.Errorf("read response: %w", err)
	}
	return resp.Result, nil
}

// Run answers a prompt using the best available compute in the swarm. If a
// remote peer is a better fit (this node has no model, or another node serves
// the requested model), the request is routed there; otherwise it runs locally.
// It returns the result and the peer id that actually served it.
func (n *Node) Run(ctx context.Context, req backend.Request) (backend.Result, string, error) {
	// Prefer local compute when this node can actually serve the request.
	localCanServe := n.backend.Available() && n.backend.Name() != "stub"
	modelMatches := req.Model == "" || n.backend.Model() == "" || n.backend.Model() == req.Model
	if localCanServe && modelMatches {
		return n.backend.Infer(ctx, req), n.Host.ID().String() + " (local)", nil
	}

	if target, _, ok := n.reg.BestInferPeer(req.Model); ok {
		res, err := n.requestRemote(ctx, target, req)
		if err != nil {
			// Fall back to local (even stub) rather than failing hard.
			return n.backend.Infer(ctx, req), n.Host.ID().String() + " (local fallback)", err
		}
		n.credits.Earn(target, 1) // reward the peer that served us
		return res, target.String(), nil
	}

	// No remote peer available: serve locally (stub if no model).
	return n.backend.Infer(ctx, req), n.Host.ID().String() + " (local)", nil
}

// inferPeers returns up to want distinct infer-capable peers for a model,
// most-free-RAM first.
func (n *Node) inferPeers(model string, want int) []peer.ID {
	type cand struct {
		id   peer.ID
		free uint64
	}
	var cands []cand
	for id, c := range n.reg.Snapshot() {
		if !c.CanInfer {
			continue
		}
		if model != "" && c.Model != "" && c.Model != model {
			continue
		}
		cands = append(cands, cand{id, c.RAMFreeMB})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].free > cands[j].free })
	out := make([]peer.ID, 0, want)
	for i := 0; i < len(cands) && i < want; i++ {
		out = append(out, cands[i].id)
	}
	return out
}

// RunRedundant sends the same request to up to `want` distinct infer-capable
// peers, compares their answers, and returns the majority result. Peers in the
// majority earn credit and an agreement; peers in the minority get a
// disagreement. This is BOINC-style redundant-execution verification for an
// open swarm where no single peer is trusted. Returns the chosen result and the
// list of peers that produced it.
func (n *Node) RunRedundant(ctx context.Context, req backend.Request, want int) (backend.Result, []string, error) {
	if want < 2 {
		res, by, err := n.Run(ctx, req)
		return res, []string{by}, err
	}
	peers := n.inferPeers(req.Model, want)
	if len(peers) == 0 {
		res, by, err := n.Run(ctx, req)
		return res, []string{by}, err
	}

	type answer struct {
		id  peer.ID
		res backend.Result
	}
	var mu sync.Mutex
	var got []answer
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p peer.ID) {
			defer wg.Done()
			r, err := n.requestRemote(ctx, p, req)
			if err != nil || r.Err != "" {
				return
			}
			mu.Lock()
			got = append(got, answer{p, r})
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	if len(got) == 0 {
		return backend.Result{Err: "no peer answered"}, nil, fmt.Errorf("no peer answered")
	}

	// Majority by normalized answer text.
	counts := map[string]int{}
	for _, g := range got {
		counts[strings.TrimSpace(g.res.Text)]++
	}
	bestText, best := "", -1
	for t, c := range counts {
		if c > best {
			best, bestText = c, t
		}
	}

	var servers []string
	var chosen backend.Result
	for _, g := range got {
		agreed := strings.TrimSpace(g.res.Text) == bestText
		n.credits.Agreement(g.id, agreed)
		if agreed {
			n.credits.Earn(g.id, 1)
			servers = append(servers, g.id.String())
			chosen = g.res
		}
	}
	return chosen, servers, nil
}
