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

// overrideSuppressionWindow is how long a published pr-override keeps a review
// for the same head SHA from being published.
//
// The bound is load-bearing, not defensive. Suppression is only safe while the
// override can still land: prod pr-review Jobs carry activeDeadlineSeconds:
// 1800, so an override that has produced no APPROVE within this window is not
// going to. Without the bound, an override whose Job died would leave its PR
// permanently unreviewed — strictly worse than the duplicate review this
// suppresses. After the window the ordinary review path takes over again.
const overrideSuppressionWindow = time.Hour

var overrideLedgerBucketName = libkv.BucketName("override-published")

type overrideLedgerRecord struct {
	TaskID      string           `json:"task_id"`
	PublishedAt libtime.DateTime `json:"published_at"`
}

// OverrideLedger records which head SHAs already carry a published pr-override,
// so that no review is published for them while the override is still in flight.
//
// It exists because the watcher has three independent producers of review tasks
// — the poll loop, the unreviewed scanner and the trigger executor — and only
// the poll loop owns the mechanical decision. A producer that cannot see that
// decision spends exactly the reviewer pod the override exists to save. Both
// observed cases came from a producer that had no way to ask:
//
//   - bborbe/kafka-topic-reader#26: the trigger executor published a review 44 s
//     after the override, on a command whose Webhook flag was false, so the
//     deferral gated on cmd.Webhook never applied.
//   - bborbe/alert#29: the unreviewed scanner published one 30 min after the
//     override, because the PR still showed no review while the override's
//     APPROVE was pending.
//
// The gate is on STATE, not on origin. "Has an override been published for this
// SHA" covers every producer that exists and every one added later; an
// allow-list of producers, command shapes or entry points would be re-opened by
// the next one, and the #26 producer could not even be identified — it arrived
// on the command topic with no inbound HTTP request logged anywhere.
//
// The ledger is in-memory and lost on restart, the same trade-off RetryGuard
// documents: a restart can only widen the window in which one duplicate slips
// through. The StatefulSet runs one replica, so it is not correct across
// replicas either.
type OverrideLedger interface {
	// Record notes that an override was published for pr. A failure is logged,
	// never returned: the override command has already been sent, and a
	// bookkeeping error must not turn that into a retry.
	Record(ctx context.Context, pr RetryGuardPR, taskID string)
	// Suppressed reports whether a review for pr's head SHA must not be
	// published because a fresh override owns it. A ledger that cannot be read
	// fails OPEN — the pre-change behaviour is to publish the review, and losing
	// a review is worse than a duplicate.
	Suppressed(ctx context.Context, pr RetryGuardPR) bool
	// SuppressedTx is the same check inside a caller-held transaction. The
	// command executor runs inside the consumer's already-held transaction and
	// memDB's mutex is not re-entrant, so it cannot open one of its own.
	SuppressedTx(ctx context.Context, tx libkv.Tx, pr RetryGuardPR) (bool, error)
}

// NewOverrideLedger returns an OverrideLedger backed by the in-process store.
// A nil db yields a ledger that records nothing and suppresses nothing, which
// is the pre-change behaviour.
func NewOverrideLedger(
	db libkv.DB,
	currentDateTime libtime.CurrentDateTimeGetter,
) OverrideLedger {
	return &overrideLedger{db: db, currentDateTime: currentDateTime}
}

type overrideLedger struct {
	db              libkv.DB
	currentDateTime libtime.CurrentDateTimeGetter
}

// Record implements OverrideLedger.
func (l *overrideLedger) Record(
	ctx context.Context,
	pr RetryGuardPR,
	taskID string,
) {
	if l == nil || l.db == nil {
		return
	}
	if err := l.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return l.record(ctx, tx, pr, taskID)
	}); err != nil {
		glog.Warningf(
			"override ledger: record failed pr=%s/%s#%d sha=%s task_id=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, taskID, err,
		)
	}
}

// Suppressed implements OverrideLedger.
func (l *overrideLedger) Suppressed(ctx context.Context, pr RetryGuardPR) bool {
	if l == nil || l.db == nil {
		return false
	}
	suppressed := false
	if err := l.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		found, err := l.suppressed(ctx, tx, pr)
		suppressed = found
		return err
	}); err != nil {
		glog.Warningf(
			"override ledger: read failed, allowing review pr=%s/%s#%d sha=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, err,
		)
		return false
	}
	return suppressed
}

// SuppressedTx implements OverrideLedger.
func (l *overrideLedger) SuppressedTx(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
) (bool, error) {
	if l == nil {
		return false, nil
	}
	return l.suppressed(ctx, tx, pr)
}

func (l *overrideLedger) record(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
	taskID string,
) error {
	now := l.currentDateTime.Now()
	bucket, err := tx.CreateBucketIfNotExists(ctx, overrideLedgerBucketName)
	if err != nil {
		return errors.Wrap(ctx, err, "create override ledger bucket")
	}
	if pruneErr := l.prune(ctx, bucket, now); pruneErr != nil {
		return pruneErr
	}
	value, err := json.Marshal(overrideLedgerRecord{TaskID: taskID, PublishedAt: now})
	if err != nil {
		return errors.Wrap(ctx, err, "encode override ledger record")
	}
	if err := bucket.Put(ctx, overrideLedgerKey(pr), value); err != nil {
		return errors.Wrap(ctx, err, "store override ledger record")
	}
	return nil
}

// suppressed answers the one question the gate asks. A record that cannot be
// decoded is treated as absent rather than as a permanent block, for the same
// reason RetryGuard.prune deletes them: one corrupt entry must never pin a SHA.
func (l *overrideLedger) suppressed(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
) (bool, error) {
	bucket, err := tx.Bucket(ctx, overrideLedgerBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return false, nil
		}
		return false, errors.Wrap(ctx, err, "get override ledger bucket")
	}
	item, err := bucket.Get(ctx, overrideLedgerKey(pr))
	if err != nil {
		return false, errors.Wrap(ctx, err, "get override ledger record")
	}
	if !item.Exists() {
		return false, nil
	}
	var record overrideLedgerRecord
	if err := item.Value(func(value []byte) error {
		return json.Unmarshal(value, &record)
	}); err != nil {
		glog.Warningf(
			"override ledger: undecodable record ignored pr=%s/%s#%d sha=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, err,
		)
		return false, nil
	}
	return l.fresh(record), nil
}

// fresh reports whether the override may still land. A publish time in the
// future (clock jump backwards) counts as fresh, matching RetryGuard.recordAge.
func (l *overrideLedger) fresh(record overrideLedgerRecord) bool {
	age := l.currentDateTime.Now().Sub(record.PublishedAt).Duration()
	if age < 0 {
		return true
	}
	return age <= overrideSuppressionWindow
}

// prune deletes every record past the suppression window and every value that
// cannot be decoded, so the ledger cannot grow without bound.
func (l *overrideLedger) prune(
	ctx context.Context,
	bucket libkv.Bucket,
	now libtime.DateTime,
) error {
	it := bucket.Iterator()
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		item := it.Item()
		var record overrideLedgerRecord
		decodeErr := item.Value(func(value []byte) error {
			return json.Unmarshal(value, &record)
		})
		if decodeErr == nil && now.Sub(record.PublishedAt).Duration() <= overrideSuppressionWindow {
			continue
		}
		if err := bucket.Delete(ctx, item.Key()); err != nil {
			return errors.Wrap(ctx, err, "prune override ledger record")
		}
	}
	return nil
}

func overrideLedgerKey(pr RetryGuardPR) []byte {
	return []byte(fmt.Sprintf("%s/%s#%d@%s", pr.Owner, pr.Repo, pr.Number, pr.HeadSHA))
}
