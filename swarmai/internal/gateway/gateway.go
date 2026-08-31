// Package gateway serves a zero-install web UI from a swarmai node: open the
// page on any phone or browser on the network and use the swarm — ask a
// question (routed and cross-checked across models) and see who is connected —
// without installing anything.
//
// This makes a browser a zero-install *consumer* of the swarm. Turning the
// browser into a full P2P *peer/contributor* (js-libp2p over the node's
// WebSocket/WebRTC transport, plus WebGPU inference via web-llm) is the next
// step; this gateway is the reachable entry point it will build on.
package gateway

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/slot-club/swarmai/internal/node"
)

//go:embed index.html
var indexHTML []byte

// Server serves the web UI and a small JSON API backed by a node.
type Server struct {
	node *node.Node
	srv  *http.Server
}

// NewServer builds a gateway bound to addr (e.g. ":8090" for all interfaces).
func NewServer(n *node.Node, addr string) *Server {
	s := &Server{node: n}
	s.srv = &http.Server{Addr: addr, Handler: s.Handler()}
	return s
}

// Handler returns the HTTP handler (exposed for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/ask", s.handleAsk)
	return mux
}

// ListenAndServe blocks serving the gateway.
func (s *Server) ListenAndServe() error { return s.srv.ListenAndServe() }

// Shutdown stops the gateway.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

type peerView struct {
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	Tier     string   `json:"tier"`
	Tags     []string `json:"tags"`
	CanInfer bool     `json:"can_infer"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var peers []peerView
	for _, c := range s.node.Peers() {
		peers = append(peers, peerView{c.Name, c.Model, c.Tier, c.Tags, c.CanInfer})
	}
	self := s.node.SelfCard()
	writeJSON(w, map[string]any{
		"self":  peerView{self.Name, self.Model, self.Tier, self.Tags, self.CanInfer},
		"peers": peers,
	})
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	prompt := r.URL.Query().Get("prompt")
	if prompt == "" {
		writeJSON(w, map[string]string{"error": "missing prompt"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	writeJSON(w, s.node.RunEnsemble(ctx, prompt))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
