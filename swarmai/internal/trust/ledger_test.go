package trust

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEarnAccumulates(t *testing.T) {
	l := NewLedger()
	p := peer.ID("peerA")
	l.Earn(p, 1)
	l.Earn(p, 2)
	got := l.Get(p)
	if got.Credits != 3 {
		t.Fatalf("credits = %d, want 3", got.Credits)
	}
	if got.Jobs != 2 {
		t.Fatalf("jobs = %d, want 2", got.Jobs)
	}
}

func TestReplicationTiers(t *testing.T) {
	l := NewLedger()
	p := peer.ID("peerB")

	// Unknown peer: maximum scrutiny.
	if got := l.Replication(p); got != 3 {
		t.Fatalf("new peer replication = %d, want 3", got)
	}

	// Three clean agreements -> tier 2.
	for i := 0; i < 3; i++ {
		l.Agreement(p, true)
	}
	if got := l.Replication(p); got != 2 {
		t.Fatalf("replication after 3 agreements = %d, want 2", got)
	}

	// Ten clean agreements -> tier 1.
	for i := 0; i < 7; i++ {
		l.Agreement(p, true)
	}
	if got := l.Replication(p); got != 1 {
		t.Fatalf("replication after 10 agreements = %d, want 1", got)
	}
}

func TestDisagreementRaisesScrutiny(t *testing.T) {
	l := NewLedger()
	p := peer.ID("peerC")
	l.Agreement(p, true)
	l.Agreement(p, false) // one bad result
	if got := l.Replication(p); got != 3 {
		t.Fatalf("replication after a disagreement = %d, want 3", got)
	}
	rep := l.Get(p)
	if rep.Agreements != 1 || rep.Disagreements != 1 {
		t.Fatalf("rep = %+v, want 1 agreement / 1 disagreement", rep)
	}
}

func TestSnapshot(t *testing.T) {
	l := NewLedger()
	l.Earn(peer.ID("a"), 1)
	l.Earn(peer.ID("b"), 1)
	if got := len(l.Snapshot()); got != 2 {
		t.Fatalf("snapshot len = %d, want 2", got)
	}
}
