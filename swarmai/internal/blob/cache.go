package blob

import (
	"container/list"
	"sync"
)

// Cache is a byte-budgeted LRU cache of chunk bytes, keyed by chunk hash. It is
// the on-device "hot expert" cache: a node keeps the recently-used experts
// resident and streams cold ones from peers on demand, so a model far larger
// than RAM stays usable as long as the active working set fits the budget.
type Cache struct {
	mu     sync.Mutex
	budget int64
	used   int64
	ll     *list.List
	items  map[string]*list.Element
}

type cacheEntry struct {
	hash string
	data []byte
}

// NewCache creates a cache holding at most budgetBytes of chunk data.
func NewCache(budgetBytes int64) *Cache {
	if budgetBytes <= 0 {
		budgetBytes = 512 << 20 // 512 MiB default working-set budget
	}
	return &Cache{
		budget: budgetBytes,
		ll:     list.New(),
		items:  make(map[string]*list.Element),
	}
}

// Get returns cached bytes for a chunk and marks it most-recently-used.
func (c *Cache) Get(hash string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[hash]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).data, true
}

// Put inserts chunk bytes, evicting least-recently-used entries to stay within
// budget. A chunk larger than the whole budget is not cached.
func (c *Cache) Put(hash string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok {
		c.ll.MoveToFront(el)
		return
	}
	if int64(len(data)) > c.budget {
		return
	}
	el := c.ll.PushFront(&cacheEntry{hash: hash, data: data})
	c.items[hash] = el
	c.used += int64(len(data))
	for c.used > c.budget {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*cacheEntry)
		c.ll.Remove(back)
		delete(c.items, ent.hash)
		c.used -= int64(len(ent.data))
	}
}

// Stats reports current usage, the budget, and the number of cached chunks.
func (c *Cache) Stats() (used, budget int64, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used, c.budget, len(c.items)
}
