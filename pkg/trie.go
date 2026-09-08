// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// Trie is a prefix tree over runes supporting insert, lookup, and prefix scan.
type Trie struct {
	children map[rune]*Trie
	terminal bool
	value    string
}

// NewTrie returns an empty trie.
func NewTrie() *Trie {
	return &Trie{children: make(map[rune]*Trie)}
}

// Insert adds word to the trie, storing value at the terminal node.
func (t *Trie) Insert(word, value string) {
	node := t
	for _, r := range word {
		next, ok := node.children[r]
		if !ok {
			next = &Trie{children: make(map[rune]*Trie)}
			node.children[r] = next
		}
		node = next
	}
	node.terminal = true
	node.value = value
}

// Lookup returns the stored value for an exact word.
func (t *Trie) Lookup(word string) (string, bool) {
	node := t
	for _, r := range word {
		next, ok := node.children[r]
		if !ok {
			return "", false
		}
		node = next
	}
	if !node.terminal {
		return "", false
	}
	return node.value, true
}

// HasPrefix reports whether any stored word starts with prefix.
func (t *Trie) HasPrefix(prefix string) bool {
	node := t
	for _, r := range prefix {
		next, ok := node.children[r]
		if !ok {
			return false
		}
		node = next
	}
	return true
}

// Remove deletes a word if present; returns whether it existed.
func (t *Trie) Remove(word string) bool {
	path := make([]*Trie, 0, len(word))
	node := t
	for _, r := range word {
		next, ok := node.children[r]
		if !ok {
			return false
		}
		path = append(path, node)
		node = next
	}
	if !node.terminal {
		return false
	}
	node.terminal = false
	node.value = ""
	for i := len(path) - 1; i >= 0; i-- {
		parent := path[i]
		if len(node.children) == 0 && !node.terminal {
			delete(parent.children, []rune(word)[i])
		}
		node = parent
	}
	return true
}

// Walk visits every stored word with its value.
func (t *Trie) Walk() map[string]string {
	out := make(map[string]string)
	var walk func(*Trie, string)
	walk = func(n *Trie, prefix string) {
		if n.terminal {
			out[prefix] = n.value
		}
		for r, child := range n.children {
			walk(child, prefix+string(r))
		}
	}
	walk(t, "")
	return out
}
