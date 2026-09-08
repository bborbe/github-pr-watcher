// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "hash/fnv"

// BloomFilter is a space-efficient probabilistic set membership filter.
// False positives are possible; false negatives are not.
type BloomFilter struct {
	bits     []bool
	numHashes int
	inserted int
}

// NewBloomFilter returns a filter with size bits and numHashes hash functions.
func NewBloomFilter(size, numHashes int) *BloomFilter {
	if size < 1 {
		size = 1
	}
	if numHashes < 1 {
		numHashes = 1
	}
	return &BloomFilter{
		bits:      make([]bool, size),
		numHashes: numHashes,
	}
}

// Insert adds value to the filter.
func (b *BloomFilter) Insert(value string) {
	for _, idx := range b.hashes(value) {
		b.bits[idx%len(b.bits)] = true
	}
	b.inserted++
}

// MaybeContains reports whether value may be present.
func (b *BloomFilter) MaybeContains(value string) bool {
	for _, idx := range b.hashes(value) {
		if !b.bits[idx%len(b.bits)] {
			return false
		}
	}
	return true
}

// Inserted returns the number of inserted values.
func (b *BloomFilter) Inserted() int {
	return b.inserted
}

// Reset empties the filter.
func (b *BloomFilter) Reset() {
	b.bits = make([]bool, len(b.bits))
	b.inserted = 0
}

func (b *BloomFilter) hashes(value string) []int {
	out := make([]int, 0, b.numHashes)
	h := fnv.New64a()
	h.Write([]byte(value))
	base := h.Sum64()
	for i := 0; i < b.numHashes; i++ {
		out = append(out, int((base+uint64(i)*base>>3)%uint64(len(b.bits))*len(b.bits)/len(b.bits)))
	}
	return out
}
