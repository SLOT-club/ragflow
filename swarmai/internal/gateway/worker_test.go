package gateway

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slot-club/swarmai/internal/backend"

	"github.com/gorilla/websocket"
)

// TestBrowserWorkerContributes: a browser (here a Go WebSocket client standing in
// for a WebGPU page) registers as a worker; the node then advertises the
// browser's model and serves swarm inference by forwarding it to the browser.
func TestBrowserWorkerContributes(t *testing.T) {
	n := testNode(t)
	srv := httptest.NewServer(NewServer(n, "").Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/worker"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial worker ws: %v", err)
	}
	defer c.Close()

	// Register as a WebGPU model worker.
	if err := c.WriteJSON(map[string]any{"type": "register", "model": "webgpu-qwen-0.5b"}); err != nil {
		t.Fatal(err)
	}

	// The browser side: answer every inference request.
	go func() {
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			if m["type"] == "infer" {
				id, _ := m["id"].(string)
				prompt, _ := m["prompt"].(string)
				_ = c.WriteJSON(map[string]any{"type": "result", "id": id, "text": "browser: " + prompt})
			}
		}
	}()

	// The node should now advertise the browser's model as its own.
	waitTrue(t, 3*time.Second, func() bool {
		card := n.SelfCard()
		return card.CanInfer && card.Model == "webgpu-qwen-0.5b"
	})

	// A swarm inference on the node is served by the browser.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, _, err := n.Run(ctx, backend.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "browser: hi" {
		t.Fatalf("answer = %q, want the browser's reply", res.Text)
	}

	// When the browser disconnects, the node stops advertising a model.
	c.Close()
	waitTrue(t, 3*time.Second, func() bool { return !n.SelfCard().CanInfer })
}

func waitTrue(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
