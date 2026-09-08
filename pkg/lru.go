// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "container/list"

// LRUCache is a fixed-capacity least-recently-used cache.
type LRUCache struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type lruEntry struct {
	key   string
	value string
}

// NewLRUCache returns a cache holding at most capacity entries.
func NewLRUCache(capacity int) *LRUCache {
	if capacity < 1 {
		capacity = 1
	}
	return &LRUCache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get returns the value for key and marks it most-recently-used.
func (c *LRUCache) Get(key string) (string, bool) {
	el, ok := c.items[key]
	if !ok {
		return "", false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).value, true
}

// Put inserts or updates key; evicts the LRU entry when over capacity.
func (c *LRUCache) Put(key, value string) {
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry).value = value
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&lruEntry{key: key, value: value})
	c.items[key] = el
	if len(c.items) > c.capacity {
		back := c.order.Back()
		if back != nil {
			c.remove(back.Value.(*lruEntry).key)
		}
	}
}

// Remove deletes key if present.
func (c *LRUCache) Remove(key string) {
	el, ok := c.items[key]
	if !ok {
		return
	}
	c.order.Remove(el)
	delete(c.items, key)
}

func (c *LRUCache) remove(key string) {
	c.Remove(key)
}

// Len returns the number of cached entries.
func (c *LRUCache) Len() int {
	return len(c.items)
}

// Clear empties the cache.
func (c *LRUCache) Clear() {
	c.items = make(map[string]*list.Element)
	c.order.Init()
}
