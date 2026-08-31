package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/slot-club/swarmai/internal/blob"

	"github.com/libp2p/go-libp2p/core/peer"
)

// resolveManifest returns a manifest, fetching it from the seeder (and caching
// it locally) if this node does not already hold it.
func (n *Node) resolveManifest(ctx context.Context, seeder peer.ID, manifestID string) (*blob.Manifest, error) {
	if m, ok := n.blobs.Manifest(manifestID); ok {
		return m, nil
	}
	m, err := n.fetchManifest(ctx, seeder, manifestID)
	if err != nil {
		return nil, err
	}
	n.blobs.AddManifest(m)
	return m, nil
}

// getChunk returns a chunk's bytes from the hot cache, the local store, or the
// seeder (in that order), caching whatever it fetches. This is the memory
// hierarchy: RAM cache → local disk → network.
func (n *Node) getChunk(ctx context.Context, seeder peer.ID, hash string) ([]byte, error) {
	if b, ok := n.experts.Get(hash); ok {
		return b, nil
	}
	if n.blobs.HasChunk(hash) {
		if b, err := n.blobs.ReadChunk(hash); err == nil {
			n.experts.Put(hash, b)
			return b, nil
		}
	}
	b, err := n.fetchChunk(ctx, seeder, hash)
	if err != nil {
		return nil, err
	}
	n.experts.Put(hash, b)
	return b, nil
}

// FetchPart streams only the chunks covering one named model part (an expert or
// layer) from a seeder, assembles them, and returns exactly that part's bytes.
// The whole model is never downloaded — only the active working set flows.
func (n *Node) FetchPart(ctx context.Context, seeder peer.ID, manifestID, part string) ([]byte, error) {
	m, err := n.resolveManifest(ctx, seeder, manifestID)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	rng, ok := m.Parts[part]
	if !ok {
		return nil, fmt.Errorf("manifest has no part %q", part)
	}
	indices, firstOff := m.ChunksForRange(rng.Offset, rng.Length)
	if len(indices) == 0 {
		return nil, fmt.Errorf("part %q maps to no chunks", part)
	}

	var assembled []byte
	for _, i := range indices {
		b, err := n.getChunk(ctx, seeder, m.Chunks[i].Hash)
		if err != nil {
			return nil, fmt.Errorf("chunk %d of part %q: %w", i, part, err)
		}
		assembled = append(assembled, b...)
	}
	start := rng.Offset - firstOff
	if start < 0 || start+rng.Length > int64(len(assembled)) {
		return nil, fmt.Errorf("part %q out of assembled range", part)
	}
	return assembled[start : start+rng.Length], nil
}

// FetchPartAuto resolves a seeder for the manifest (optionally hinted) and
// fetches the part.
func (n *Node) FetchPartAuto(ctx context.Context, manifestID, part, fromHint string) ([]byte, string, error) {
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
		b, err := n.FetchPart(ctx, pid, manifestID, part)
		if err == nil {
			return b, pid.String(), nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

// Prefetch warms the cache with several parts concurrently — the MoE router's
// predicted next experts — so they are resident before they are needed.
func (n *Node) Prefetch(ctx context.Context, seeder peer.ID, manifestID string, parts []string) {
	var wg sync.WaitGroup
	for _, p := range parts {
		wg.Add(1)
		go func(part string) {
			defer wg.Done()
			_, _ = n.FetchPart(ctx, seeder, manifestID, part)
		}(p)
	}
	wg.Wait()
}

// ShareModelWithParts shares a model file and annotates it with a part layout
// so peers can fetch individual experts/layers.
func (n *Node) ShareModelWithParts(path, name string, parts map[string]blob.Range) (*blob.Manifest, error) {
	m, err := n.blobs.ShareFile(path, name)
	if err != nil {
		return nil, err
	}
	if len(parts) > 0 {
		if err := n.blobs.AttachParts(m.ID, parts); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// ExpertCacheStats reports the hot-expert cache usage.
func (n *Node) ExpertCacheStats() (used, budget int64, count int) {
	return n.experts.Stats()
}
