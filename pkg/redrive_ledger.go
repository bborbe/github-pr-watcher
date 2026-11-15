// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// redrivePendingBound is how long a pending re-drive stays closeable.
//
// The bound exists so a re-drive whose review never lands cannot pin an entry
// in the map for the process lifetime. It is deliberately generous: it only has
// to outlive a review run, and prod pr-review Jobs carry
// activeDeadlineSeconds: 1800. A re-drive still pending after a day has failed
// for a reason the close path cannot fix.
const redrivePendingBound = 24 * time.Hour

var redriveLedgerBucketName = libkv.BucketName("redrive-pending")

type redriveLedgerRecord struct {
	OriginalTaskID string           `json:"original_task_id"`
	PublishedAt    libtime.DateTime `json:"published_at"`
}

// RedriveLedger records the original record a re-drive superseded, keyed on the
// pull request and head SHA the re-drive was published for.
//
// It exists because a re-drive cannot close the record it replaces at publish
// time. `PublishRedrive` lands the review on a SIBLING record — the title must
// change or the controller's title-path dedup drops the command — so the
// original stays parked at human_review with no verdict, which is the shape
// that keeps the unreviewed queue red.
//
// Closing it at publish time would be worse than leaving it open: the queue
// would drain whether or not the sibling ever ran, so a re-drive that never
// produced a pod would read as a cleared review. The close is therefore gated
// on the only evidence that the review actually happened — a reviewer verdict
// at the head SHA, which the unreviewed scanner already computes every cycle.
// This ledger is what carries the original's task id from the publish to that
// verdict.
//
// The ledger is in-memory and lost on restart, the same trade-off RetryGuard
// and OverrideLedger document: a restart between the re-drive and its verdict
// leaves the original open, so the failure is a stuck record rather than a
// false clean one. That direction is deliberate — this ledger exists to stop a
// record reading clean before its review exists.
type RedriveLedger interface {
	// Record notes that a re-drive superseded originalTaskID for pr's head SHA.
	// A failure is logged, never returned: the re-drive command has already been
	// sent, and a bookkeeping error must not turn that into a retry.
	Record(ctx context.Context, pr RetryGuardPR, originalTaskID string)
	// Pending returns the original task id a re-drive superseded for pr's head
	// SHA, and whether one is outstanding. A ledger that cannot be read reports
	// nothing pending: the pre-change behaviour is to close no record, and a
	// read failure must never fabricate a close.
	Pending(ctx context.Context, pr RetryGuardPR) (string, bool)
	// Clear forgets the entry once the original has been closed, so the close is
	// attempted at most once per re-drive.
	Clear(ctx context.Context, pr RetryGuardPR)
}

// NewRedriveLedger returns a RedriveLedger backed by the in-process store. A
// nil db yields a ledger that records nothing and reports nothing pending,
// which is the pre-change behaviour.
func NewRedriveLedger(
	db libkv.DB,
	currentDateTime libtime.CurrentDateTimeGetter,
) RedriveLedger {
	return &redriveLedger{db: db, currentDateTime: currentDateTime}
}

type redriveLedger struct {
	db              libkv.DB
	currentDateTime libtime.CurrentDateTimeGetter
}

// Record implements RedriveLedger.
func (l *redriveLedger) Record(
	ctx context.Context,
	pr RetryGuardPR,
	originalTaskID string,
) {
	if l == nil || l.db == nil || originalTaskID == "" {
		return
	}
	if err := l.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return l.record(ctx, tx, pr, originalTaskID)
	}); err != nil {
		glog.Warningf(
			"redrive ledger: record failed pr=%s/%s#%d sha=%s task_id=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, originalTaskID, err,
		)
	}
}

// Pending implements RedriveLedger.
func (l *redriveLedger) Pending(
	ctx context.Context,
	pr RetryGuardPR,
) (string, bool) {
	if l == nil || l.db == nil {
		return "", false
	}
	var taskID string
	found := false
	if err := l.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var readErr error
		taskID, found, readErr = l.pending(ctx, tx, pr)
		return readErr
	}); err != nil {
		glog.Warningf(
			"redrive ledger: read failed, closing nothing pr=%s/%s#%d sha=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, err,
		)
		return "", false
	}
	return taskID, found
}

// Clear implements RedriveLedger.
func (l *redriveLedger) Clear(ctx context.Context, pr RetryGuardPR) {
	if l == nil || l.db == nil {
		return
	}
	if err := l.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		bucket, err := tx.Bucket(ctx, redriveLedgerBucketName)
		if err != nil {
			if errors.Is(err, libkv.BucketNotFoundError) {
				return nil
			}
			return errors.Wrap(ctx, err, "get redrive ledger bucket")
		}
		return bucket.Delete(ctx, redriveLedgerKey(pr))
	}); err != nil {
		glog.Warningf(
			"redrive ledger: clear failed pr=%s/%s#%d sha=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, err,
		)
	}
}

func (l *redriveLedger) record(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
	originalTaskID string,
) error {
	now := l.currentDateTime.Now()
	bucket, err := tx.CreateBucketIfNotExists(ctx, redriveLedgerBucketName)
	if err != nil {
		return errors.Wrap(ctx, err, "create redrive ledger bucket")
	}
	if pruneErr := l.prune(ctx, bucket, now); pruneErr != nil {
		return pruneErr
	}
	value, err := json.Marshal(redriveLedgerRecord{
		OriginalTaskID: originalTaskID,
		PublishedAt:    now,
	})
	if err != nil {
		return errors.Wrap(ctx, err, "encode redrive ledger record")
	}
	if err := bucket.Put(ctx, redriveLedgerKey(pr), value); err != nil {
		return errors.Wrap(ctx, err, "store redrive ledger record")
	}
	return nil
}

// pending answers the one question the close path asks. A record that cannot be
// decoded is treated as absent rather than as a permanent block, for the same
// reason OverrideLedger.prune deletes them: one corrupt entry must never pin a
// SHA.
func (l *redriveLedger) pending(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
) (string, bool, error) {
	bucket, err := tx.Bucket(ctx, redriveLedgerBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return "", false, nil
		}
		return "", false, errors.Wrap(ctx, err, "get redrive ledger bucket")
	}
	item, err := bucket.Get(ctx, redriveLedgerKey(pr))
	if err != nil {
		return "", false, errors.Wrap(ctx, err, "get redrive ledger record")
	}
	if !item.Exists() {
		return "", false, nil
	}
	var record redriveLedgerRecord
	if err := item.Value(func(value []byte) error {
		return json.Unmarshal(value, &record)
	}); err != nil {
		glog.Warningf(
			"redrive ledger: undecodable record ignored pr=%s/%s#%d sha=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, err,
		)
		return "", false, nil
	}
	if record.OriginalTaskID == "" || !l.fresh(record) {
		return "", false, nil
	}
	return record.OriginalTaskID, true, nil
}

// fresh reports whether the pending re-drive may still be closed. A publish
// time in the future (clock jump backwards) counts as fresh, matching
// OverrideLedger.fresh.
func (l *redriveLedger) fresh(record redriveLedgerRecord) bool {
	age := l.currentDateTime.Now().Sub(record.PublishedAt).Duration()
	if age < 0 {
		return true
	}
	return age <= redrivePendingBound
}

// prune deletes every record past the bound and every value that cannot be
// decoded, so the ledger cannot grow without bound.
func (l *redriveLedger) prune(
	ctx context.Context,
	bucket libkv.Bucket,
	now libtime.DateTime,
) error {
	it := bucket.Iterator()
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		item := it.Item()
		var record redriveLedgerRecord
		decodeErr := item.Value(func(value []byte) error {
			return json.Unmarshal(value, &record)
		})
		if decodeErr == nil && now.Sub(record.PublishedAt).Duration() <= redrivePendingBound {
			continue
		}
		if err := bucket.Delete(ctx, item.Key()); err != nil {
			return errors.Wrap(ctx, err, "prune redrive ledger record")
		}
	}
	return nil
}

func redriveLedgerKey(pr RetryGuardPR) []byte {
	return []byte(fmt.Sprintf("%s/%s#%d@%s", pr.Owner, pr.Repo, pr.Number, pr.HeadSHA))
}
