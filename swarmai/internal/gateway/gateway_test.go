package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/node"
)

func testNode(t *testing.T) *node.Node {
	t.Helper()
	n, err := node.New(context.Background(), node.Config{
		ListenPort:   0,
		Backend:      backend.Stub{},
		IdentityPath: filepath.Join(t.TempDir(), "id.key"),
		Name:         "gw-test",
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestGatewayServesPageAndAPI(t *testing.T) {
	srv := httptest.NewServer(NewServer(testNode(t), "").Handler())
	defer srv.Close()

	// The page loads and is HTML.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("index content-type = %q", ct)
	}
	if !strings.Contains(string(body), "swarmai") {
		t.Fatal("index page missing title")
	}

	// Status returns self + peers.
	var st struct {
		Self  peerView   `json:"self"`
		Peers []peerView `json:"peers"`
	}
	getJSON(t, srv.URL+"/api/status", &st)
	if st.Self.Name != "gw-test" {
		t.Fatalf("status self name = %q", st.Self.Name)
	}

	// Ask returns an ensemble result (local stub answer, unchecked).
	var ans struct {
		Answer     string `json:"answer"`
		Confidence string `json:"confidence"`
	}
	getJSON(t, srv.URL+"/api/ask?prompt=hello", &ans)
	if !strings.Contains(ans.Answer, "hello") {
		t.Fatalf("ask answer = %q, want it to echo the prompt", ans.Answer)
	}
	if ans.Confidence != "unchecked" {
		t.Fatalf("ask confidence = %q, want unchecked (no peers)", ans.Confidence)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
