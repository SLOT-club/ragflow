// Package control exposes a small loopback HTTP API so the CLI can query a
// running node daemon (status, peers) and submit prompts (run).
package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/node"
)

// Server is the loopback control API for a node.
type Server struct {
	node *node.Node
	http *http.Server
}

// NewServer builds a control server bound to 127.0.0.1:addr.
func NewServer(n *node.Node, addr string) *Server {
	s := &Server{node: n}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/peers", s.handlePeers)
	mux.HandleFunc("/run", s.handleRun)
	mux.HandleFunc("/share", s.handleShare)
	mux.HandleFunc("/fetch", s.handleFetch)
	mux.HandleFunc("/models", s.handleModels)
	mux.HandleFunc("/cell/prepare", s.handleCellPrepare)
	mux.HandleFunc("/draft", s.handleDraft)
	mux.HandleFunc("/credits", s.handleCredits)
	mux.HandleFunc("/part", s.handlePart)
	mux.HandleFunc("/cache", s.handleCache)
	mux.HandleFunc("/ask", s.handleAsk)
	mux.HandleFunc("/invite", s.handleInvite)
	s.http = &http.Server{Addr: addr, Handler: mux}
	return s
}

// ListenAndServe blocks serving the control API.
func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }

// Shutdown stops the control API.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"self":  s.node.SelfCard(),
		"addrs": s.node.Addrs(),
		"peers": len(s.node.Peers()),
	})
}

func (s *Server) handlePeers(w http.ResponseWriter, _ *http.Request) {
	cards := s.node.Peers()
	list := make([]node.CapabilityCard, 0, len(cards))
	for _, c := range cards {
		list = append(list, c)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	prompt := r.URL.Query().Get("prompt")
	if prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing prompt"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	req := backend.Request{Prompt: prompt, Model: r.URL.Query().Get("model")}

	// Redundant execution: fan out to N peers and take the majority.
	if red := r.URL.Query().Get("redundancy"); red != "" {
		if n, _ := strconv.Atoi(red); n >= 2 {
			res, servers, err := s.node.RunRedundant(ctx, req, n)
			resp := map[string]any{"result": res, "agreeing_peers": servers, "redundancy": n}
			if err != nil {
				resp["route_error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	res, servedBy, err := s.node.Run(ctx, req)
	resp := map[string]any{"result": res, "served_by": servedBy}
	if err != nil {
		resp["route_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDraft answers with draft→verify (M2): draft locally, verify on a peer.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	prompt := r.URL.Query().Get("prompt")
	if prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing prompt"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	v, servedBy, err := s.node.RunSpeculative(ctx, prompt, r.URL.Query().Get("model"))
	resp := map[string]any{"verdict": v, "verified_by": servedBy}
	if err != nil {
		resp["route_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCredits returns this node's local reputation ledger.
func (s *Server) handleCredits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.node.Credits())
}

// handlePart fetches one named model part (expert/layer) on demand, streaming
// only the chunks that cover it.
func (s *Server) handlePart(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	part := r.URL.Query().Get("part")
	if id == "" || part == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need id and part"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	b, from, err := s.node.FetchPartAuto(ctx, id, part, r.URL.Query().Get("from"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	used, budget, count := s.node.ExpertCacheStats()
	writeJSON(w, http.StatusOK, map[string]any{
		"part":         part,
		"bytes":        len(b),
		"preview_hex":  hex.EncodeToString(b[:min(32, len(b))]),
		"fetched_from": from,
		"cache":        map[string]any{"used": used, "budget": budget, "chunks": count},
	})
}

// handleCache reports the hot-expert cache usage.
func (s *Server) handleCache(w http.ResponseWriter, _ *http.Request) {
	used, budget, count := s.node.ExpertCacheStats()
	writeJSON(w, http.StatusOK, map[string]any{"used": used, "budget": budget, "chunks": count})
}

// handleInvite returns a join token another device can use to join the swarm.
func (s *Server) handleInvite(w http.ResponseWriter, _ *http.Request) {
	token := s.node.InviteToken()
	writeJSON(w, http.StatusOK, map[string]string{
		"token": token,
		"join":  "swarmai node start --join " + token,
		"addrs": strings.Join(s.node.Addrs(), " "),
	})
}

// handleAsk answers as a routed, cross-checked ensemble ("combo").
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	prompt := r.URL.Query().Get("prompt")
	if prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing prompt"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, s.node.RunEnsemble(ctx, prompt))
}

// handleShare chunks a local file and starts seeding it, returning its manifest.
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	m, err := s.node.ShareModel(path, r.URL.Query().Get("name"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleFetch streams a model by manifest id from a seeding peer into a file.
func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	out := r.URL.Query().Get("out")
	if id == "" || out == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need id and out"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	m, servedBy, err := s.node.FetchModelAuto(ctx, id, out, r.URL.Query().Get("from"), 8)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifest": m, "fetched_from": servedBy, "out": out})
}

// handleModels lists what this node seeds and what peers are seeding.
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	remote := map[string]any{}
	for id, c := range s.node.Peers() {
		if len(c.Seeds) > 0 {
			remote[id.String()] = c.Seeds
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"local":  s.node.LocalSeeds(),
		"remote": remote,
	})
}

// handleCellPrepare sets up secure tunnels to worker peers and returns the
// llama.cpp flags to run a coordinator over the cell.
func (s *Server) handleCellPrepare(w http.ResponseWriter, r *http.Request) {
	workers := r.URL.Query().Get("workers")
	if workers == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing workers (comma-separated peer ids)"})
		return
	}
	rpcArg, split, err := s.node.PrepareCell(strings.Split(workers, ","))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rpc":          rpcArg,
		"tensor_split": split,
		"launch_hint":  "llama-server -m <model.gguf> --rpc " + rpcArg + " --tensor-split " + split,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// DefaultControlAddr returns the loopback control address for a given node port.
func DefaultControlAddr(nodePort int) string {
	return fmt.Sprintf("127.0.0.1:%d", nodePort+1)
}
