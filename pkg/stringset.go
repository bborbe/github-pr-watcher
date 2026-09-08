// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "sort"

// StringSet is a small set of strings with stable iteration order.
// Real implementation used by the e2e oversized-PR test.
type StringSet struct {
	items map[string]struct{}
}

// NewStringSet returns an empty StringSet.
func NewStringSet() *StringSet {
	return &StringSet{items: make(map[string]struct{})}
}

// NewStringSetFrom returns a StringSet pre-populated with the given values.
func NewStringSetFrom(values []string) *StringSet {
	s := NewStringSet()
	for _, v := range values {
		s.Add(v)
	}
	return s
}

// Add inserts a value; duplicate inserts are no-ops.
func (s *StringSet) Add(value string) {
	s.items[value] = struct{}{}
}

// Remove deletes a value.
func (s *StringSet) Remove(value string) {
	delete(s.items, value)
}

// Contains reports whether value is present.
func (s *StringSet) Contains(value string) bool {
	_, ok := s.items[value]
	return ok
}

// Len returns the number of distinct values.
func (s *StringSet) Len() int {
	return len(s.items)
}

// Values returns the values in sorted order.
func (s *StringSet) Values() []string {
	out := make([]string, 0, len(s.items))
	for v := range s.items {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Merge adds all values from other.
func (s *StringSet) Merge(other *StringSet) {
	if other == nil {
		return
	}
	for v := range other.items {
		s.Add(v)
	}
}

// Intersection returns a new set with values present in both sets.
func (s *StringSet) Intersection(other *StringSet) *StringSet {
	out := NewStringSet()
	if other == nil {
		return out
	}
	for v := range s.items {
		if other.Contains(v) {
			out.Add(v)
		}
	}
	return out
}

// Union returns a new set with values present in either set.
func (s *StringSet) Union(other *StringSet) *StringSet {
	out := NewStringSetFrom(s.Values())
	if other != nil {
		out.Merge(other)
	}
	return out
}

// Difference returns a new set with values in s but not in other.
func (s *StringSet) Difference(other *StringSet) *StringSet {
	out := NewStringSet()
	if other == nil {
		return NewStringSetFrom(s.Values())
	}
	for v := range s.items {
		if !other.Contains(v) {
			out.Add(v)
		}
	}
	return out
}

// Subset reports whether every value of s is in other.
func (s *StringSet) Subset(other *StringSet) bool {
	if other == nil {
		return false
	}
	for v := range s.items {
		if !other.Contains(v) {
			return false
		}
	}
	return true
}

// Equal reports whether both sets hold exactly the same values.
func (s *StringSet) Equal(other *StringSet) bool {
	if other == nil || len(s.items) != len(other.items) {
		return false
	}
	for v := range s.items {
		if !other.Contains(v) {
			return false
		}
	}
	return true
}

// Clear removes all values.
func (s *StringSet) Clear() {
	s.items = make(map[string]struct{})
}

// Clone returns a deep copy.
func (s *StringSet) Clone() *StringSet {
	return NewStringSetFrom(s.Values())
}
