// Package backend abstracts the local inference engine a node can offer to the
// swarm. A node with a real model configures a llama-server (OpenAI-compatible)
// backend; a node without one still participates in the network as a
// router/relay/seeder using the stub backend.
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Request is one inference request travelling between nodes.
type Request struct {
	Prompt    string `json:"prompt"`
	Model     string `json:"model,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// Result is the outcome of running a Request on some node.
type Result struct {
	Text  string `json:"text"`
	Model string `json:"model,omitempty"`
	Err   string `json:"error,omitempty"`
}

// Backend is a local inference engine.
type Backend interface {
	// Name is a short identifier reported in capability cards.
	Name() string
	// Available reports whether the backend can currently serve requests.
	Available() bool
	// Model is the model this backend serves, empty if none.
	Model() string
	// Infer runs one request locally.
	Infer(ctx context.Context, req Request) Result
}

// Stub is a backend for nodes that hold no model. It keeps the node useful as a
// relay/seeder and makes the network testable without any model present.
type Stub struct{}

func (Stub) Name() string    { return "stub" }
func (Stub) Available() bool { return true }
func (Stub) Model() string   { return "" }
func (Stub) Infer(_ context.Context, req Request) Result {
	return Result{
		Text:  fmt.Sprintf("[stub node: no local model; echo] %s", req.Prompt),
		Model: "stub",
	}
}

// LlamaServer talks to a local llama-server (or any OpenAI-compatible endpoint)
// at BaseURL. It applies the compatibility switches learned from real
// deployments: max_tokens (not max_completion_tokens), no developer role, and
// thinking disabled so a reasoning model does not return an empty content.
type LlamaServer struct {
	BaseURL   string
	ModelName string
	client    *http.Client
}

// NewLlamaServer builds a llama-server backend. If model is empty, the model
// name is auto-detected from the server's /v1/models endpoint, so pointing
// swarmai at a running llama-server is enough — no --model needed.
func NewLlamaServer(baseURL, model string) *LlamaServer {
	l := &LlamaServer{
		BaseURL:   baseURL,
		ModelName: model,
		client:    &http.Client{Timeout: 5 * time.Minute},
	}
	if l.ModelName == "" {
		l.ModelName = l.detectModel()
	}
	return l
}

// detectModel asks the server for the loaded model's id (OpenAI /v1/models).
func (l *LlamaServer) detectModel() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/v1/models", nil)
	if err != nil {
		return ""
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data) == 0 {
		return ""
	}
	id := out.Data[0].ID
	if i := strings.LastIndexAny(id, `/\`); i >= 0 { // trim a path to a bare name
		id = id[i+1:]
	}
	return id
}

func (l *LlamaServer) Name() string  { return "llama-server" }
func (l *LlamaServer) Model() string { return l.ModelName }

// Available probes the /health endpoint; a llama-server exposes it once ready.
func (l *LlamaServer) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.BaseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model              string         `json:"model,omitempty"`
	Messages           []chatMessage  `json:"messages"`
	MaxTokens          int            `json:"max_tokens,omitempty"`
	Temperature        float64        `json:"temperature"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Infer runs one chat completion against the llama-server.
func (l *LlamaServer) Infer(ctx context.Context, req Request) Result {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 1024
	}
	body := chatRequest{
		Model:       req.Model,
		Messages:    []chatMessage{{Role: "user", Content: req.Prompt}},
		MaxTokens:   maxTok,
		Temperature: 0.7,
		// Disable thinking so content is not consumed entirely by reasoning.
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return Result{Err: fmt.Sprintf("marshal: %v", err)}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return Result{Err: fmt.Sprintf("request: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(httpReq)
	if err != nil {
		return Result{Err: fmt.Sprintf("llama-server unreachable: %v", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Result{Err: fmt.Sprintf("llama-server %d: %s", resp.StatusCode, string(raw))}
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{Err: fmt.Sprintf("decode: %v", err)}
	}
	if parsed.Error != nil {
		return Result{Err: parsed.Error.Message}
	}
	if len(parsed.Choices) == 0 {
		return Result{Err: "no choices returned"}
	}
	return Result{Text: parsed.Choices[0].Message.Content, Model: l.ModelName}
}
