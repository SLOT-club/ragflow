package blob

import "testing"

func TestCacheLRUEviction(t *testing.T) {
	c := NewCache(300)
	c.Put("a", make([]byte, 100))
	c.Put("b", make([]byte, 100))
	c.Put("c", make([]byte, 100)) // used = 300, at budget

	// Touch "a" so it becomes most-recently-used; "b" is now the LRU.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should be present")
	}
	c.Put("d", make([]byte, 100)) // must evict the LRU ("b")

	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should have been kept (recently used)")
	}
	if _, ok := c.Get("d"); !ok {
		t.Fatal("d should be present")
	}
	if used, _, n := c.Stats(); used != 300 || n != 3 {
		t.Fatalf("stats used=%d n=%d, want 300/3", used, n)
	}
}

func TestCacheRejectsOversize(t *testing.T) {
	c := NewCache(50)
	c.Put("x", make([]byte, 100))
	if _, ok := c.Get("x"); ok {
		t.Fatal("a chunk larger than the budget must not be cached")
	}
}

func TestCacheDuplicatePut(t *testing.T) {
	c := NewCache(1000)
	c.Put("a", make([]byte, 100))
	c.Put("a", make([]byte, 100)) // duplicate: no double counting
	if used, _, n := c.Stats(); used != 100 || n != 1 {
		t.Fatalf("stats used=%d n=%d, want 100/1", used, n)
	}
}
