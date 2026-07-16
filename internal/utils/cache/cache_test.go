package cache

import (
	"sync"
	"testing"
)

func TestCacheLifecycle(t *testing.T) {
	c := New[string, int](4)

	if c.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", c.Len())
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("Get(missing) found an unexpected value")
	}

	c.Set("one", 1)
	c.Set("two", 2)
	c.Set("one", 10)
	if got, ok := c.Get("one"); !ok || got != 10 {
		t.Fatalf("Get(one) = %d, %v, want 10, true", got, ok)
	}
	if !c.Exists("one", "two") || c.Exists("one", "missing") {
		t.Fatal("Exists() returned an unexpected result")
	}
	if c.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", c.Len())
	}

	all := c.GetAll()
	if len(all) != 2 || all["one"] != 10 || all["two"] != 2 {
		t.Fatalf("GetAll() = %#v", all)
	}
	delete(all, "one")
	if !c.Exists("one") {
		t.Fatal("mutating GetAll() result changed the cache")
	}

	if deleted := c.Del("one", "missing"); deleted != 1 {
		t.Fatalf("Del() = %d, want 1", deleted)
	}
	c.Clear()
	if c.Len() != 0 || len(c.GetAll()) != 0 {
		t.Fatal("Clear() did not empty the cache")
	}
}

func TestNewUsesDefaultShardCount(t *testing.T) {
	c := New[int, int](0)
	implementation, ok := c.(*cache[int, int])
	if !ok {
		t.Fatalf("New() returned %T, want *cache", c)
	}
	if len(implementation.shards) != 1024 {
		t.Fatalf("default shard count = %d, want 1024", len(implementation.shards))
	}
}

func TestCacheUsesEveryNonPowerOfTwoShard(t *testing.T) {
	c := New[int, int](3).(*cache[int, int])
	for i := 0; i < 1000; i++ {
		c.Set(i, i)
	}
	for index, shard := range c.shards {
		if shard.len() == 0 {
			t.Fatalf("shard %d was never selected", index)
		}
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := New[int, int](16)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			c.Set(value, value)
			if got, ok := c.Get(value); !ok || got != value {
				t.Errorf("Get(%d) = %d, %v", value, got, ok)
			}
		}(i)
	}
	wg.Wait()
	if c.Len() != 100 {
		t.Fatalf("Len() = %d, want 100", c.Len())
	}
}
