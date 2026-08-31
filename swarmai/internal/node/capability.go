package node

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slot-club/swarmai/internal/blob"

	"github.com/libp2p/go-libp2p/core/peer"
)

// CapabilityCard is what every node periodically gossips about itself: what
// hardware it has, what it can serve, and under what resource policy. It is the
// unit of "who can do what" in the swarm.
type CapabilityCard struct {
	PeerID     string          `json:"peer_id"`
	Name       string          `json:"name"`
	OS         string          `json:"os"`
	Arch       string          `json:"arch"`
	CPUs       int             `json:"cpus"`
	RAMTotalMB uint64          `json:"ram_total_mb"`
	RAMFreeMB  uint64          `json:"ram_free_mb"`
	Backend    string          `json:"backend"`   // "llama-server" | "stub"
	Model      string          `json:"model"`     // served model, if any
	CanInfer   bool            `json:"can_infer"` // backend available now
	Schedule   string          `json:"schedule"`  // idle|night|always|manual
	Seeds      []blob.SeedInfo `json:"seeds"`     // models this node seeds for streaming
	UnixTime   int64           `json:"unix_time"`
}

// stale returns true if the card is older than ttl.
func (c CapabilityCard) stale(ttl time.Duration) bool {
	return time.Since(time.Unix(c.UnixTime, 0)) > ttl
}

// detectHost fills in the parts of a card that describe the local machine.
func detectHost() CapabilityCard {
	total, free := memInfoMB()
	return CapabilityCard{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPUs:       runtime.NumCPU(),
		RAMTotalMB: total,
		RAMFreeMB:  free,
		UnixTime:   time.Now().Unix(),
	}
}

// memInfoMB reads total and available RAM in MB. It parses /proc/meminfo on
// Linux and returns zeros elsewhere (the field is advisory, not load-bearing).
func memInfoMB() (total, free uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = kb / 1024
		case "MemAvailable:":
			free = kb / 1024
		}
	}
	return total, free
}

// Registry holds the most recent capability card seen for each peer.
type Registry struct {
	mu    sync.RWMutex
	cards map[peer.ID]CapabilityCard
	ttl   time.Duration
}

// NewRegistry creates a registry that treats cards older than ttl as stale.
func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{cards: make(map[peer.ID]CapabilityCard), ttl: ttl}
}

// Update records the latest card for a peer.
func (r *Registry) Update(id peer.ID, card CapabilityCard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cards[id] = card
}

// Snapshot returns all non-stale cards.
func (r *Registry) Snapshot() map[peer.ID]CapabilityCard {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[peer.ID]CapabilityCard, len(r.cards))
	for id, c := range r.cards {
		if !c.stale(r.ttl) {
			out[id] = c
		}
	}
	return out
}

// BestInferPeer picks the best remote peer able to serve a request. Selection
// prefers a node that can infer, matches the requested model when one is asked,
// and has the most free RAM as a rough capacity proxy. Returns ok=false if no
// suitable remote peer exists.
func (r *Registry) BestInferPeer(model string) (peer.ID, CapabilityCard, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var bestID peer.ID
	var best CapabilityCard
	found := false
	for id, c := range r.cards {
		if c.stale(r.ttl) || !c.CanInfer {
			continue
		}
		if model != "" && c.Model != "" && c.Model != model {
			continue
		}
		if !found || c.RAMFreeMB > best.RAMFreeMB {
			bestID, best, found = id, c, true
		}
	}
	return bestID, best, found
}

// SeedersFor returns peers currently seeding the given manifest id.
func (r *Registry) SeedersFor(manifestID string) []peer.ID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []peer.ID
	for id, c := range r.cards {
		if c.stale(r.ttl) {
			continue
		}
		for _, s := range c.Seeds {
			if s.ID == manifestID {
				out = append(out, id)
				break
			}
		}
	}
	return out
}
