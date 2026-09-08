// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "sync"

// StringQueue is a goroutine-safe FIFO queue of strings.
type StringQueue struct {
	mu    sync.Mutex
	items []string
}

// NewStringQueue returns an empty queue.
func NewStringQueue() *StringQueue {
	return &StringQueue{}
}

// Push appends a value to the tail.
func (q *StringQueue) Push(value string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, value)
}

// Pop removes and returns the head value; ok is false when empty.
func (q *StringQueue) Pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return "", false
	}
	head := q.items[0]
	q.items = q.items[1:]
	return head, true
}

// Peek returns the head value without removing it.
func (q *StringQueue) Peek() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return "", false
	}
	return q.items[0], true
}

// Len returns the number of queued values.
func (q *StringQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// IsEmpty reports whether the queue holds no values.
func (q *StringQueue) IsEmpty() bool {
	return q.Len() == 0
}

// Drain removes and returns all values in order.
func (q *StringQueue) Drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}
