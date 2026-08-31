package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockLlama serves the endpoints swarmai probes: /health and /v1/models, plus a
// chat completion.
func mockLlama(t *testing.T, modelID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + modelID + `","object":"model"}]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLlamaAutoDetectModel(t *testing.T) {
	srv := mockLlama(t, "/models/qwen3.5-4b.gguf")
	l := NewLlamaServer(srv.URL, "") // empty model → auto-detect
	if l.Model() != "qwen3.5-4b.gguf" {
		t.Fatalf("Model() = %q, want the basename qwen3.5-4b.gguf", l.Model())
	}
	if !l.Available() {
		t.Fatal("Available() should be true against a healthy server")
	}
	res := l.Infer(context.Background(), Request{Prompt: "x"})
	if res.Err != "" || res.Text != "hi" {
		t.Fatalf("Infer = %+v", res)
	}
}

func TestLlamaExplicitModelWins(t *testing.T) {
	srv := mockLlama(t, "detected-name")
	l := NewLlamaServer(srv.URL, "my-model")
	if l.Model() != "my-model" {
		t.Fatalf("explicit model overridden: %q", l.Model())
	}
}
