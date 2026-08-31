package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/invite"
)

// TestJoinViaInviteToken proves the onboarding flow: one node prints a token,
// another decodes it, boots with it as its bootstrap, and the two discover each
// other — no manual multiaddr copying.
func TestJoinViaInviteToken(t *testing.T) {
	host := newTestNode(t, backend.Stub{})

	token := host.InviteToken()
	peers, err := invite.Decode(token)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	joiner, err := New(context.Background(), Config{
		ListenPort:   0,
		Backend:      backend.Stub{},
		IdentityPath: filepath.Join(t.TempDir(), "id.key"),
		Bootstrap:    peers,
	})
	if err != nil {
		t.Fatalf("New joiner: %v", err)
	}
	t.Cleanup(func() { _ = joiner.Close() })

	time.Sleep(2 * time.Second)
	host.AnnounceNow()
	joiner.AnnounceNow()

	waitFor(t, "joiner and host to discover each other", 20*time.Second, func() bool {
		_, seesHost := joiner.reg.Snapshot()[host.Host.ID()]
		return seesHost
	})
}
