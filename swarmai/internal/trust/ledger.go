// Package trust holds a node's local, non-monetary reputation ledger: how much
// each peer has contributed to us (kudos, AI-Horde style) and how reliable it
// has proven (agreement rate under redundant execution, BOINC style). It is
// deliberately local and non-transferable — no blockchain, no currency — and
// drives priority and how often we re-verify a peer (adaptive replication).
package trust

import (
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Rep is what we track about one peer.
type Rep struct {
	PeerID        string `json:"peer_id"`
	Credits       int64  `json:"credits"`       // kudos: useful work done for us
	Jobs          int    `json:"jobs"`          // jobs it served us
	Agreements    int    `json:"agreements"`    // times it agreed with the majority
	Disagreements int    `json:"disagreements"` // times it disagreed (possible bad result)
}

// Ledger is a concurrency-safe per-peer reputation store.
type Ledger struct {
	mu sync.RWMutex
	m  map[peer.ID]*Rep
}

// NewLedger creates an empty ledger.
func NewLedger() *Ledger { return &Ledger{m: make(map[peer.ID]*Rep)} }

func (l *Ledger) rep(p peer.ID) *Rep {
	r, ok := l.m[p]
	if !ok {
		r = &Rep{PeerID: p.String()}
		l.m[p] = r
	}
	return r
}

// Earn credits a peer for useful work and counts a served job.
func (l *Ledger) Earn(p peer.ID, n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rep(p)
	r.Credits += n
	r.Jobs++
}

// Agreement records whether a peer agreed with the majority on a redundant job.
func (l *Ledger) Agreement(p peer.ID, agreed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rep(p)
	if agreed {
		r.Agreements++
	} else {
		r.Disagreements++
	}
}

// Replication returns how many independent peers a job for this peer should be
// sent to before its result is trusted — high for new/unreliable peers, low for
// proven ones (BOINC adaptive replication). A peer with any disagreements stays
// at maximum scrutiny until it rebuilds a clean streak.
func (l *Ledger) Replication(p peer.ID) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	r, ok := l.m[p]
	if !ok {
		return 3
	}
	if r.Disagreements > 0 && r.Agreements < r.Disagreements*10 {
		return 3
	}
	switch {
	case r.Agreements >= 10:
		return 1
	case r.Agreements >= 3:
		return 2
	default:
		return 3
	}
}

// Get returns a copy of a peer's record.
func (l *Ledger) Get(p peer.ID) Rep {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if r, ok := l.m[p]; ok {
		return *r
	}
	return Rep{PeerID: p.String()}
}

// Snapshot returns copies of all records.
func (l *Ledger) Snapshot() []Rep {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Rep, 0, len(l.m))
	for _, r := range l.m {
		out = append(out, *r)
	}
	return out
}
