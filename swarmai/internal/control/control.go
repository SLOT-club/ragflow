// Package control exposes a small loopback HTTP API so the CLI can query a
// running node daemon (status, peers) and submit prompts (run).
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	res, servedBy, err := s.node.Run(ctx, backend.Request{
		Prompt: prompt,
		Model:  r.URL.Query().Get("model"),
	})
	resp := map[string]any{"result": res, "served_by": servedBy}
	if err != nil {
		resp["route_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
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
