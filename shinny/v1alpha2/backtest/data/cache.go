package data

import (
	"container/list"
	"sync"
)

type CacheStats struct {
	Entries   int
	UsedBytes int64
	MaxBytes  int64
}

type cacheEntry struct {
	key   string
	value any
	size  int64
}

// Cache is a thread-safe LRU cache with optional maxBytes enforcement.
// When maxBytes <= 0, eviction is disabled.
type Cache struct {
	mu        sync.RWMutex
	maxBytes  int64
	usedBytes int64
	items     map[string]*list.Element
	order     *list.List // front = most-recently-used
}

func NewCache(maxBytes int64) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(elem)
	return elem.Value.(*cacheEntry).value, true
}

func (c *Cache) Set(key string, value any, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		// Update existing entry
		entry := elem.Value.(*cacheEntry)
		c.usedBytes -= entry.size
		entry.value = value
		entry.size = size
		c.usedBytes += size
		c.order.MoveToFront(elem)
	} else {
		entry := &cacheEntry{key: key, value: value, size: size}
		elem := c.order.PushFront(entry)
		c.items[key] = elem
		c.usedBytes += size
	}

	// Evict LRU entries if over budget
	if c.maxBytes > 0 {
		for c.usedBytes > c.maxBytes && c.order.Len() > 0 {
			c.evictLRU()
		}
	}
}

func (c *Cache) evictLRU() {
	tail := c.order.Back()
	if tail == nil {
		return
	}
	entry := tail.Value.(*cacheEntry)
	c.order.Remove(tail)
	delete(c.items, entry.key)
	c.usedBytes -= entry.size
}

func (c *Cache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Entries:   len(c.items),
		UsedBytes: c.usedBytes,
		MaxBytes:  c.maxBytes,
	}
}
