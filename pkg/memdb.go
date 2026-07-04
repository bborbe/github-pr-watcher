// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sort"
	"sync"

	libkv "github.com/bborbe/kv"
)

// NewMemDB returns a session-scoped, in-process libkv.DB implementation.
// It exists because libkv itself does not ship a public NewMemDB constructor
// (the upstream bborbe/kv package exposes only NewDBWithMetrics). The
// github-pr watcher's command consumer needs an offset store; this in-memory
// implementation gives it one without requiring a PVC (see spec 066 AC 9
// rationale: replay-from-OffsetOldest on restart is safe because the
// downstream CreateTaskCommand is idempotent via derived task_id).
//
// The implementation is intentionally minimal — it implements only the
// methods the offset-store / consumer wiring touches: Update, View, Sync,
// Close, Remove, Stats, StatsDetailed. Iterator / ListBucketNames return
// empty results because no caller in this package uses them.
func NewMemDB() libkv.DB {
	return &memDB{
		buckets: make(map[string]map[string][]byte),
	}
}

type memDB struct {
	mu      sync.RWMutex
	buckets map[string]map[string][]byte
	stats   libkv.Stats
}

func (d *memDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fn(ctx, &memTx{db: d})
}

func (d *memDB) View(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return fn(ctx, &memTx{db: d})
}

func (d *memDB) Sync() error {
	return nil
}

func (d *memDB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buckets = nil
	return nil
}

func (d *memDB) Remove() error {
	return d.Close()
}

func (d *memDB) Stats(ctx context.Context) (*libkv.Stats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := d.stats
	return &s, nil
}

func (d *memDB) StatsDetailed(ctx context.Context) (*libkv.Stats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := d.stats
	return &s, nil
}

type memTx struct {
	db *memDB
}

func (t *memTx) Bucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if t.db.buckets == nil {
		return nil, libkv.BucketNotFoundError
	}
	b, ok := t.db.buckets[name.String()]
	if !ok {
		return nil, libkv.BucketNotFoundError
	}
	return &memBucket{b: b}, nil
}

func (t *memTx) CreateBucket(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if _, ok := t.db.buckets[name.String()]; ok {
		return nil, libkv.BucketAlreadyExistsError
	}
	t.db.buckets[name.String()] = make(map[string][]byte)
	return &memBucket{b: t.db.buckets[name.String()]}, nil
}

func (t *memTx) CreateBucketIfNotExists(
	ctx context.Context,
	name libkv.BucketName,
) (libkv.Bucket, error) {
	if _, ok := t.db.buckets[name.String()]; !ok {
		t.db.buckets[name.String()] = make(map[string][]byte)
	}
	return &memBucket{b: t.db.buckets[name.String()]}, nil
}

func (t *memTx) DeleteBucket(ctx context.Context, name libkv.BucketName) error {
	delete(t.db.buckets, name.String())
	return nil
}

func (t *memTx) ListBucketNames(ctx context.Context) (libkv.BucketNames, error) {
	names := make(libkv.BucketNames, 0, len(t.db.buckets))
	for name := range t.db.buckets {
		names = append(names, libkv.BucketName(name))
	}
	sort.Slice(names, func(i, j int) bool { return names[i].String() < names[j].String() })
	return names, nil
}

type memBucket struct {
	b map[string][]byte
}

func (b *memBucket) Put(ctx context.Context, key []byte, value []byte) error {
	b.b[string(key)] = append([]byte(nil), value...)
	return nil
}

func (b *memBucket) Get(ctx context.Context, key []byte) (libkv.Item, error) {
	v, ok := b.b[string(key)]
	if !ok {
		return libkv.NewByteItem(key, nil), nil
	}
	return libkv.NewByteItem(key, v), nil
}

func (b *memBucket) Delete(ctx context.Context, key []byte) error {
	delete(b.b, string(key))
	return nil
}

func (b *memBucket) Iterator() libkv.Iterator {
	return &memIterator{}
}

func (b *memBucket) IteratorReverse() libkv.Iterator {
	return &memIterator{}
}

type memIterator struct{}

func (i *memIterator) Close() {}

func (i *memIterator) Item() libkv.Item { return libkv.NewByteItem(nil, nil) }

func (i *memIterator) Next() {}

func (i *memIterator) Valid() bool { return false }

func (i *memIterator) Rewind() {}

func (i *memIterator) Seek(key []byte) {}
