package node

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/slot-club/swarmai/internal/backend"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuitclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
)

func rawHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.EnableRelay(),
	)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// TestPublicRelayCarriesStream: a swarmai node running as a relay lets a peer
// that is only reachable through the relay be dialed and served over a stream —
// the mechanism that makes a NAT'd phone reachable from anywhere.
func TestPublicRelayCarriesStream(t *testing.T) {
	relayNode, err := New(context.Background(), Config{
		ListenPort:   0,
		Backend:      backend.Stub{},
		IdentityPath: filepath.Join(t.TempDir(), "id.key"),
		Relay:        true,
	})
	if err != nil {
		t.Fatalf("relay node: %v", err)
	}
	t.Cleanup(func() { _ = relayNode.Close() })
	relayInfo := peer.AddrInfo{ID: relayNode.Host.ID(), Addrs: relayNode.Host.Addrs()}

	nat := rawHost(t) // only reachable through the relay
	dialer := rawHost(t)

	// An echo protocol on the NAT'd peer stands in for swarmai's stream protocols.
	const proto = "/swarmai-test/echo/1"
	nat.SetStreamHandler(proto, func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})

	ctx := context.Background()
	if err := nat.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("nat->relay: %v", err)
	}
	if err := dialer.Connect(ctx, relayInfo); err != nil {
		t.Fatalf("dialer->relay: %v", err)
	}

	// The NAT'd peer reserves a slot on the relay.
	if _, err := circuitclient.Reserve(ctx, nat, relayInfo); err != nil {
		t.Fatalf("reserve on relay: %v", err)
	}

	// The dialer reaches the NAT'd peer only via the circuit address.
	circuit, err := ma.NewMultiaddr("/p2p/" + relayNode.Host.ID().String() + "/p2p-circuit/p2p/" + nat.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	dialer.Peerstore().AddAddrs(nat.ID(), []ma.Multiaddr{circuit}, time.Hour)

	sctx := network.WithAllowLimitedConn(ctx, "relay-test")
	s, err := dialer.NewStream(sctx, nat.ID(), proto)
	if err != nil {
		t.Fatalf("dial through relay: %v", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := s.Write([]byte("relayed")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "relayed" {
		t.Fatalf("echo through relay = %q, want relayed", buf)
	}
}
