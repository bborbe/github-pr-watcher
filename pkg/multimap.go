// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// StringMultiMap maps a string key to an ordered list of string values.
type StringMultiMap struct {
	items map[string][]string
}

// NewStringMultiMap returns an empty multimap.
func NewStringMultiMap() *StringMultiMap {
	return &StringMultiMap{items: make(map[string][]string)}
}

// Add appends value under key, creating the list if absent.
func (m *StringMultiMap) Add(key, value string) {
	m.items[key] = append(m.items[key], value)
}

// Get returns the values for key; nil when absent.
func (m *StringMultiMap) Get(key string) []string {
	return m.items[key]
}

// HasKey reports whether key holds at least one value.
func (m *StringMultiMap) HasKey(key string) bool {
	_, ok := m.items[key]
	return ok
}

// Keys returns all keys in arbitrary order.
func (m *StringMultiMap) Keys() []string {
	out := make([]string, 0, len(m.items))
	for k := range m.items {
		out = append(out, k)
	}
	return out
}

// RemoveKey deletes all values under key.
func (m *StringMultiMap) RemoveKey(key string) {
	delete(m.items, key)
}

// Len returns the number of distinct keys.
func (m *StringMultiMap) Len() int {
	return len(m.items)
}

// TotalValues returns the sum of all per-key list lengths.
func (m *StringMultiMap) TotalValues() int {
	total := 0
	for _, vs := range m.items {
		total += len(vs)
	}
	return total
}
