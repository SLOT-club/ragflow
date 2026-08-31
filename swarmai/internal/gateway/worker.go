package gateway

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/slot-club/swarmai/internal/backend"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// The page is served same-origin; allow any origin so a browser opened via
	// the node's LAN/public address can still register as a worker.
	CheckOrigin: func(*http.Request) bool { return true },
}

// browserWorker is a Backend whose inference runs in a connected browser (e.g.
// via WebGPU). The gateway forwards each request over the worker's WebSocket and
// waits for the reply, so the browser contributes real compute to the swarm.
type browserWorker struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	model   string

	seq     uint64
	mu      sync.Mutex
	pending map[string]chan backend.Result
}

func newBrowserWorker(conn *websocket.Conn, model string) *browserWorker {
	if model == "" {
		model = "browser-webgpu"
	}
	return &browserWorker{conn: conn, model: model, pending: make(map[string]chan backend.Result)}
}

func (b *browserWorker) Name() string    { return "browser-webgpu" }
func (b *browserWorker) Available() bool { return true }
func (b *browserWorker) Model() string   { return b.model }

// Infer sends the request to the browser and blocks until it replies or ctx ends.
func (b *browserWorker) Infer(ctx context.Context, req backend.Request) backend.Result {
	id := strconv.FormatUint(atomic.AddUint64(&b.seq, 1), 10)
	ch := make(chan backend.Result, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	msg := map[string]any{"type": "infer", "id": id, "prompt": req.Prompt, "max_tokens": req.MaxTokens}
	b.writeMu.Lock()
	err := b.conn.WriteJSON(msg)
	b.writeMu.Unlock()
	if err != nil {
		return backend.Result{Err: fmt.Sprintf("browser worker write: %v", err)}
	}

	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		return backend.Result{Err: "browser worker timed out"}
	}
}

// deliver routes a browser result to the waiting Infer call.
func (b *browserWorker) deliver(id string, res backend.Result) {
	b.mu.Lock()
	ch := b.pending[id]
	b.mu.Unlock()
	if ch != nil {
		ch <- res
	}
}

// handleWorker upgrades a browser connection to a worker: it registers the
// browser's model as this node's backend and forwards inference to it until the
// socket closes.
func (s *Server) handleWorker(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// First message registers the worker.
	var reg struct {
		Type  string `json:"type"`
		Model string `json:"model"`
	}
	if err := conn.ReadJSON(&reg); err != nil || reg.Type != "register" {
		return
	}
	worker := newBrowserWorker(conn, reg.Model)
	s.node.SwapBackend(worker)
	log.Printf("browser worker joined: model=%q", worker.Model())
	defer func() {
		s.node.RestoreBackend()
		log.Printf("browser worker left")
	}()

	// Read results until the socket closes.
	for {
		var msg struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Type == "result" {
			worker.deliver(msg.ID, backend.Result{Text: msg.Text, Model: worker.Model(), Err: msg.Error})
		}
	}
}
