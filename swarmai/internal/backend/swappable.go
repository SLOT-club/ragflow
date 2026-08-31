package backend

import (
	"context"
	"sync"
)

// Swappable is a Backend whose inner implementation can be replaced at runtime.
// A node is built around one so an external contributor — e.g. a browser
// running a model in WebGPU — can become the node's model while connected, and
// the node reverts to its base backend when the contributor leaves.
type Swappable struct {
	mu    sync.RWMutex
	base  Backend
	inner Backend
}

// NewSwappable wraps a base backend (used until something is swapped in).
func NewSwappable(base Backend) *Swappable {
	if base == nil {
		base = Stub{}
	}
	return &Swappable{base: base, inner: base}
}

// Set replaces the active backend.
func (s *Swappable) Set(b Backend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b == nil {
		b = s.base
	}
	s.inner = b
}

// Restore reverts to the base backend.
func (s *Swappable) Restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = s.base
}

func (s *Swappable) current() Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner
}

func (s *Swappable) Name() string                                { return s.current().Name() }
func (s *Swappable) Available() bool                             { return s.current().Available() }
func (s *Swappable) Model() string                               { return s.current().Model() }
func (s *Swappable) Infer(ctx context.Context, r Request) Result { return s.current().Infer(ctx, r) }
