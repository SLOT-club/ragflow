// Package node implements a swarmai peer: a libp2p host that discovers other
// peers (mDNS on the LAN, Kademlia DHT on the WAN), gossips its hardware
// capabilities, and can both offer and consume inference over the swarm.
package node

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/blob"
	"github.com/slot-club/swarmai/internal/cell"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/multiformats/go-multiaddr"
)

// rendezvous is the shared string swarmai peers use to find each other on the DHT.
const rendezvous = "swarmai/rendezvous/1"

// capTopic is the GossipSub topic on which capability cards are published.
const capTopic = "swarmai/caps/1"

// Config configures a node.
type Config struct {
	Name         string // human-readable node name
	IdentityPath string // where the persistent private key lives
	ListenPort   int    // TCP+QUIC listen port (0 = random)
	Backend      backend.Backend
	Bootstrap    []string // extra bootstrap multiaddrs (WAN); DHT defaults added too
	Schedule     string   // idle|night|always|manual

	// RPCWorker turns this node into a llama.cpp RPC worker for LAN cells.
	RPCWorker    bool
	RPCServerBin string // path to llama.cpp rpc-server (optional)
	RPCPort      int    // loopback port for rpc-server (default 50052)
}

// Node is a running swarmai peer.
type Node struct {
	Host     host.Host
	dht      *dht.IpfsDHT
	ps       *pubsub.PubSub
	topic    *pubsub.Topic
	reg      *Registry
	blobs    *blob.Store
	backend  backend.Backend
	name     string
	schedule string

	mu    sync.Mutex
	coord *cell.Coordinator
}

// New builds and starts a node: host, discovery, gossip, and the infer handler.
func New(ctx context.Context, cfg Config) (*Node, error) {
	priv, err := loadOrCreateIdentity(cfg.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	port := cfg.ListenPort
	listen := []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
		fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(listen...),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
	)
	if err != nil {
		return nil, fmt.Errorf("new host: %w", err)
	}

	be := cfg.Backend
	if be == nil {
		be = backend.Stub{}
	}
	sched := cfg.Schedule
	if sched == "" {
		sched = "always"
	}

	n := &Node{
		Host:     h,
		reg:      NewRegistry(90 * time.Second),
		blobs:    blob.NewStore(),
		backend:  be,
		name:     cfg.Name,
		schedule: sched,
	}

	h.SetStreamHandler(InferProtocol, n.handleInferStream)
	h.SetStreamHandler(BlobProtocol, n.handleBlobStream)

	if err := n.setupDHT(ctx, cfg.Bootstrap); err != nil {
		return nil, err
	}
	if err := n.setupPubSub(ctx); err != nil {
		return nil, err
	}
	if err := n.setupMDNS(); err != nil {
		return nil, err
	}
	n.startDiscoveryAdvertise(ctx)
	n.startCapabilityLoops(ctx)

	if cfg.RPCWorker {
		port := cfg.RPCPort
		if port == 0 {
			port = 50052
		}
		if _, err := cell.StartRPCServer(ctx, cfg.RPCServerBin, port); err != nil {
			log.Printf("rpc-server not started: %v (tunnel still targets 127.0.0.1:%d)", err, port)
		}
		cell.RegisterWorker(h, fmt.Sprintf("127.0.0.1:%d", port))
		log.Printf("rpc worker active: /swarmai/rpc tunnels to loopback 127.0.0.1:%d", port)
	}

	return n, nil
}

// PrepareCell sets up secure loopback→libp2p tunnels to the given worker peers
// and returns the llama.cpp flags to launch a coordinator over the cell: the
// --rpc value (loopback tunnel addresses) and a --tensor-split weighted by each
// member's free RAM. The tunnels stay open until the next PrepareCell or Close.
func (n *Node) PrepareCell(workerIDs []string) (rpcArg, tensorSplit string, err error) {
	if len(workerIDs) == 0 {
		return "", "", fmt.Errorf("no worker peers given")
	}
	snap := n.reg.Snapshot()
	workers := make([]cell.Worker, 0, len(workerIDs))
	for _, wid := range workerIDs {
		pid, derr := peer.Decode(wid)
		if derr != nil {
			return "", "", fmt.Errorf("bad worker id %q: %w", wid, derr)
		}
		card := snap[pid]
		workers = append(workers, cell.Worker{Peer: pid, Name: card.Name, RAMFreeMB: card.RAMFreeMB})
	}
	coord := cell.NewCoordinator(n.Host, workers)
	if err := coord.Setup(); err != nil {
		return "", "", err
	}
	n.mu.Lock()
	if n.coord != nil {
		n.coord.Close()
	}
	n.coord = coord
	n.mu.Unlock()
	return coord.RPCArg(), coord.TensorSplit(detectHost().RAMFreeMB), nil
}

// setupDHT starts a Kademlia DHT in server mode and bootstraps it.
func (n *Node) setupDHT(ctx context.Context, extra []string) error {
	kad, err := dht.New(ctx, n.Host, dht.Mode(dht.ModeServer))
	if err != nil {
		return fmt.Errorf("dht: %w", err)
	}
	n.dht = kad
	if err := kad.Bootstrap(ctx); err != nil {
		return fmt.Errorf("dht bootstrap: %w", err)
	}

	// Connect to public bootstrap peers plus any user-provided ones (best effort).
	peers := dht.DefaultBootstrapPeers
	for _, a := range extra {
		if ai, err := parsePeerAddr(a); err == nil {
			peers = append(peers, ai.Addrs...)
			n.Host.Peerstore().AddAddrs(ai.ID, ai.Addrs, time.Hour)
			go func(p peer.AddrInfo) { _ = n.Host.Connect(ctx, p) }(*ai)
		}
	}
	for _, pa := range peers {
		if ai, err := peer.AddrInfoFromP2pAddr(pa); err == nil {
			go func(p peer.AddrInfo) {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				_ = n.Host.Connect(cctx, p)
			}(*ai)
		}
	}
	return nil
}

// setupPubSub joins the capability gossip topic.
func (n *Node) setupPubSub(ctx context.Context) error {
	ps, err := pubsub.NewGossipSub(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("gossipsub: %w", err)
	}
	topic, err := ps.Join(capTopic)
	if err != nil {
		return fmt.Errorf("join topic: %w", err)
	}
	n.ps = ps
	n.topic = topic
	return nil
}

// mdnsNotifee connects to peers found on the LAN.
type mdnsNotifee struct {
	h   host.Host
	ctx context.Context
}

func (m *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == m.h.ID() {
		return
	}
	cctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	if err := m.h.Connect(cctx, pi); err != nil {
		log.Printf("mdns connect %s: %v", pi.ID.String()[:12], err)
	} else {
		log.Printf("mdns connected %s", pi.ID.String()[:12])
	}
}

// setupMDNS starts LAN discovery. This is what makes the "home cell" instant.
func (n *Node) setupMDNS() error {
	svc := mdns.NewMdnsService(n.Host, rendezvous, &mdnsNotifee{h: n.Host, ctx: context.Background()})
	return svc.Start()
}

// startDiscoveryAdvertise advertises and periodically searches the DHT so WAN
// peers using the same rendezvous find each other.
func (n *Node) startDiscoveryAdvertise(ctx context.Context) {
	rd := drouting.NewRoutingDiscovery(n.dht)
	dutil.Advertise(ctx, rd, rendezvous)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peerCh, err := rd.FindPeers(ctx, rendezvous)
				if err != nil {
					continue
				}
				for p := range peerCh {
					if p.ID == n.Host.ID() || len(p.Addrs) == 0 {
						continue
					}
					go func(pi peer.AddrInfo) {
						cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
						defer cancel()
						_ = n.Host.Connect(cctx, pi)
					}(p)
				}
			}
		}
	}()
}

// startCapabilityLoops publishes this node's card and ingests others'.
func (n *Node) startCapabilityLoops(ctx context.Context) {
	sub, err := n.topic.Subscribe()
	if err != nil {
		log.Printf("subscribe caps: %v", err)
		return
	}

	// Ingest loop.
	go func() {
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				return
			}
			if msg.ReceivedFrom == n.Host.ID() {
				continue
			}
			var card CapabilityCard
			if err := json.Unmarshal(msg.Data, &card); err != nil {
				continue
			}
			if from, err := peer.Decode(card.PeerID); err == nil {
				n.reg.Update(from, card)
			}
		}
	}()

	// Publish loop.
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		n.publishCard(ctx) // publish once immediately
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n.publishCard(ctx)
			}
		}
	}()
}

// publishCard gossips this node's current capability card.
func (n *Node) publishCard(ctx context.Context) {
	card := detectHost()
	card.PeerID = n.Host.ID().String()
	card.Name = n.name
	card.Backend = n.backend.Name()
	card.Model = n.backend.Model()
	card.CanInfer = n.backend.Available() && n.backend.Name() != "stub"
	card.Schedule = n.schedule
	card.Seeds = n.blobs.Seeds()
	card.UnixTime = time.Now().Unix()

	data, err := json.Marshal(card)
	if err != nil {
		return
	}
	if err := n.topic.Publish(ctx, data); err != nil {
		log.Printf("publish card: %v", err)
	}
}

// Peers returns the current known capability cards (excluding self).
func (n *Node) Peers() map[peer.ID]CapabilityCard { return n.reg.Snapshot() }

// SelfCard returns this node's current capability card.
func (n *Node) SelfCard() CapabilityCard {
	card := detectHost()
	card.PeerID = n.Host.ID().String()
	card.Name = n.name
	card.Backend = n.backend.Name()
	card.Model = n.backend.Model()
	card.CanInfer = n.backend.Available() && n.backend.Name() != "stub"
	card.Schedule = n.schedule
	card.Seeds = n.blobs.Seeds()
	return card
}

// FetchModelAuto streams a model by manifest id, choosing a seeding peer. If
// fromHint is a valid peer id it is tried first; otherwise the registry is
// consulted for a peer advertising the manifest.
func (n *Node) FetchModelAuto(ctx context.Context, manifestID, outPath, fromHint string, window int) (*blob.Manifest, string, error) {
	var candidates []peer.ID
	if fromHint != "" {
		if pid, err := peer.Decode(fromHint); err == nil {
			candidates = append(candidates, pid)
		}
	}
	candidates = append(candidates, n.reg.SeedersFor(manifestID)...)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("no peer is seeding manifest %s", manifestID)
	}
	var lastErr error
	for _, pid := range candidates {
		m, err := n.FetchModel(ctx, pid, manifestID, outPath, window)
		if err == nil {
			return m, pid.String(), nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

// Addrs returns the full multiaddrs another node can dial to reach this one.
func (n *Node) Addrs() []string {
	var out []string
	for _, a := range n.Host.Addrs() {
		out = append(out, fmt.Sprintf("%s/p2p/%s", a, n.Host.ID()))
	}
	return out
}

// Close shuts the node down.
func (n *Node) Close() error {
	n.mu.Lock()
	if n.coord != nil {
		n.coord.Close()
	}
	n.mu.Unlock()
	if n.dht != nil {
		_ = n.dht.Close()
	}
	return n.Host.Close()
}

// loadOrCreateIdentity loads a persistent Ed25519 key, creating it on first run
// so a node keeps a stable peer ID across restarts.
func loadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".swarmai", "identity.key")
	}
	if data, err := os.ReadFile(path); err == nil {
		return crypto.UnmarshalPrivateKey(data)
	}
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	data, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// parsePeerAddr parses a /p2p/ multiaddr into peer info.
func parsePeerAddr(s string) (*peer.AddrInfo, error) {
	ma, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(ma)
}
