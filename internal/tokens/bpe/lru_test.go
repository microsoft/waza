package bpe

import "testing"

func TestLRUGetSet(t *testing.T) {
	cache := NewLRUCache(3)
	cache.Set("key1", []int{1})
	cache.Set("key2", []int{2})
	cache.Set("key3", []int{3})

	if got, ok := cache.Get("key1"); !ok || got[0] != 1 {
		t.Fatalf("expected key1=1, got %v (ok=%v)", got, ok)
	}
	if got, ok := cache.Get("key2"); !ok || got[0] != 2 {
		t.Fatalf("expected key2=2, got %v (ok=%v)", got, ok)
	}
	if got, ok := cache.Get("key3"); !ok || got[0] != 3 {
		t.Fatalf("expected key3=3, got %v (ok=%v)", got, ok)
	}
}

func TestLRUGetNonExistent(t *testing.T) {
	cache := NewLRUCache(3)
	cache.Set("key1", []int{1})
	cache.Set("key2", []int{2})
	cache.Set("key3", []int{3})

	if _, ok := cache.Get("key4"); ok {
		t.Fatalf("expected missing key")
	}
}

func TestLRUSetExisting(t *testing.T) {
	cache := NewLRUCache(3)
	cache.Set("key1", []int{1})
	cache.Set("key2", []int{2})
	cache.Set("key3", []int{3})
	cache.Set("key2", []int{20})

	if got, ok := cache.Get("key2"); !ok || got[0] != 20 {
		t.Fatalf("expected key2=20, got %v (ok=%v)", got, ok)
	}
}

func TestLRUEviction(t *testing.T) {
	cache := NewLRUCache(3)
	cache.Set("key1", []int{1})
	cache.Set("key2", []int{2})
	cache.Set("key3", []int{3})
	cache.Set("key4", []int{4})

	if _, ok := cache.Get("key1"); ok {
		t.Fatalf("expected key1 to be evicted")
	}
	if got, ok := cache.Get("key2"); !ok || got[0] != 2 {
		t.Fatalf("expected key2=2, got %v (ok=%v)", got, ok)
	}
	if got, ok := cache.Get("key3"); !ok || got[0] != 3 {
		t.Fatalf("expected key3=3, got %v (ok=%v)", got, ok)
	}
	if got, ok := cache.Get("key4"); !ok || got[0] != 4 {
		t.Fatalf("expected key4=4, got %v (ok=%v)", got, ok)
	}
}
