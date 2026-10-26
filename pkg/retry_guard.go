// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// RetryGuardPR identifies one publishable review target.
type RetryGuardPR struct {
	Owner   string
	Repo    string
	Number  int
	HeadSHA string
}

// RetryGuardAction is what the guard wants the caller to do.
type RetryGuardAction string

const (
	RetryGuardActionCreate RetryGuardAction = "create"
	RetryGuardActionSkip   RetryGuardAction = "skip"
)

// RetryGuardReason is why the guard chose its action.
type RetryGuardReason string

const (
	RetryGuardReasonCreate                     RetryGuardReason = "create"
	RetryGuardReasonCreateForceOverrodeVerdict RetryGuardReason = "create_force_overrode_verdict"
	RetryGuardReasonCreateStaleRecordIgnored   RetryGuardReason = "create_stale_record_ignored"
	RetryGuardReasonSkipInflight               RetryGuardReason = "skip_inflight"
)

// RetryGuardDecision is the guard's verdict for one forced re-review.
type RetryGuardDecision struct {
	Action         RetryGuardAction
	Reason         RetryGuardReason
	ExistingTaskID string
	RecordAge      libtime.Duration
}

// retryGuardStaleAfter is how long a published record keeps suppressing a
// forced re-review. Prod pr-review Jobs carry activeDeadlineSeconds: 1800, so
// no legitimate run outlives this bound.
const retryGuardStaleAfter = time.Hour

var retryGuardBucketName = libkv.BucketName("retry-guard-published")

type retryGuardRecord struct {
	TaskID      string           `json:"task_id"`
	PublishedAt libtime.DateTime `json:"published_at"`
}

//counterfeiter:generate -o ../mocks/retry_guard.go --fake-name RetryGuard . RetryGuard

// RetryGuard suppresses a forced re-review whose review for the same head SHA
// is still in flight.
//
// The ledger of published reviews is in-memory and lost on restart. That
// limitation is accepted: a restart can only widen the window in which one
// duplicate slips through, and a restart is also the only immediate way to
// clear a stuck record. The restart-independent backstop is the reviewer
// verdict check — a commit that already carries a verdict from a configured
// reviewer is allowed to re-run even when a record survives. The StatefulSet
// runs one replica, so the ledger is not correct across replicas.
type RetryGuard interface {
	// Check decides whether a forced re-review may be published. It never
	// writes to the ledger; only Record does.
	Check(ctx context.Context, tx libkv.Tx, pr RetryGuardPR) (RetryGuardDecision, error)
	// Record stores that a review was published for pr, so a later Check
	// suppresses a duplicate while it is fresh.
	Record(ctx context.Context, tx libkv.Tx, pr RetryGuardPR, taskID string) error
}

// PublishRecorder records that a review task was published for one head SHA,
// so the guard's Check can suppress a duplicate forced retry.
//
// It exists for the publish paths that hold no transaction of their own — the
// poll loop and the unreviewed scanner both publish through TaskPublisher. The
// command executor must NOT use a recorder: it runs inside the consumer's
// already-held transaction and memDB's mutex is not re-entrant, so it records
// through its own tx instead.
type PublishRecorder interface {
	RecordPublish(ctx context.Context, pr RetryGuardPR, taskID string)
}

// NewPublishRecorder returns a PublishRecorder that writes into the same ledger
// Check reads, one transaction per record.
func NewPublishRecorder(db libkv.DB, guard RetryGuard) PublishRecorder {
	return &publishRecorder{db: db, guard: guard}
}

type publishRecorder struct {
	db    libkv.DB
	guard RetryGuard
}

// RecordPublish writes one ledger entry. A failure is logged, never returned:
// the review task has already been sent, and the caller's publish result must
// not be turned into a retry by a bookkeeping error.
func (r *publishRecorder) RecordPublish(
	ctx context.Context,
	pr RetryGuardPR,
	taskID string,
) {
	if r == nil || r.db == nil || r.guard == nil {
		return
	}
	if err := r.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return r.guard.Record(ctx, tx, pr, taskID)
	}); err != nil {
		glog.Warningf(
			"retry guard: record publish failed pr=%s/%s#%d sha=%s task_id=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, taskID, err,
		)
	}
}

// NewRetryGuard returns a RetryGuard backed by the in-process ledger.
func NewRetryGuard(
	ghClient GitHubClient,
	metrics Metrics,
	reviewerLogins []string,
	currentDateTime libtime.CurrentDateTimeGetter,
) RetryGuard {
	return &retryGuard{
		ghClient:        ghClient,
		metrics:         metrics,
		reviewerLogins:  reviewerLogins,
		currentDateTime: currentDateTime,
	}
}

type retryGuard struct {
	ghClient        GitHubClient
	metrics         Metrics
	reviewerLogins  []string
	currentDateTime libtime.CurrentDateTimeGetter
}

func (g *retryGuard) Check(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
) (RetryGuardDecision, error) {
	record, found, err := g.readRecord(ctx, tx, pr)
	if err != nil {
		return RetryGuardDecision{}, err
	}
	if !found {
		return RetryGuardDecision{
			Action: RetryGuardActionCreate,
			Reason: RetryGuardReasonCreate,
		}, nil
	}

	age := g.recordAge(record.PublishedAt)
	if age.Duration() > retryGuardStaleAfter {
		ageSec := int64(age.Duration() / time.Second)
		glog.Infof("retry guard: stale in-flight record ignored pr=%s/%s#%d sha=%s"+
			" task_id=%s age_seconds=%d",
			pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID, ageSec)
		return RetryGuardDecision{
			Action:    RetryGuardActionCreate,
			Reason:    RetryGuardReasonCreateStaleRecordIgnored,
			RecordAge: age,
		}, nil
	}

	return g.decideFresh(ctx, pr, record, age)
}

// decideFresh applies the verdict check to a fresh ledger record: a verdict at
// the head SHA overrides the suppression, an unavailable verdict lookup makes
// the guard permissive, and otherwise the in-flight run suppresses this one.
func (g *retryGuard) decideFresh(
	ctx context.Context,
	pr RetryGuardPR,
	record retryGuardRecord,
	age libtime.Duration,
) (RetryGuardDecision, error) {
	reviews, err := g.ghClient.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		glog.Warningf(
			"retry guard: verdict lookup failed pr=%s/%s#%d err=%v",
			pr.Owner, pr.Repo, pr.Number, err,
		)
		return RetryGuardDecision{
			Action: RetryGuardActionCreate,
			Reason: RetryGuardReasonCreate,
		}, nil
	}

	for _, review := range reviews {
		if review.CommitID == pr.HeadSHA &&
			matchesReviewerLogin(review.AuthorLogin, g.reviewerLogins) {
			glog.Infof("retry guard: force overrode verdict pr=%s/%s#%d sha=%s",
				pr.Owner, pr.Repo, pr.Number, pr.HeadSHA)
			return RetryGuardDecision{
				Action:    RetryGuardActionCreate,
				Reason:    RetryGuardReasonCreateForceOverrodeVerdict,
				RecordAge: age,
			}, nil
		}
	}

	g.metrics.IncPRPublished("inflight_skipped")
	ageSec := int64(age.Duration() / time.Second)
	glog.Infof("retry guard: suppressed in-flight run pr=%s/%s#%d sha=%s"+
		" task_id=%s age_seconds=%d",
		pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID, ageSec)
	return RetryGuardDecision{
		Action:         RetryGuardActionSkip,
		Reason:         RetryGuardReasonSkipInflight,
		ExistingTaskID: record.TaskID,
		RecordAge:      age,
	}, errors.Wrapf(
		ctx, cdb.ErrCommandObjectSkipped,
		"in-flight review already published pr=%s/%s#%d sha=%s task_id=%s",
		pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID,
	)
}

func (g *retryGuard) Record(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
	taskID string,
) error {
	now := g.currentDateTime.Now()
	bucket, err := tx.CreateBucketIfNotExists(ctx, retryGuardBucketName)
	if err != nil {
		return errors.Wrap(ctx, err, "create retry guard bucket")
	}
	if pruneErr := g.prune(ctx, bucket, now); pruneErr != nil {
		return pruneErr
	}
	value, err := json.Marshal(retryGuardRecord{TaskID: taskID, PublishedAt: now})
	if err != nil {
		return errors.Wrap(ctx, err, "encode retry guard record")
	}
	if err := bucket.Put(ctx, retryGuardKey(pr), value); err != nil {
		return errors.Wrap(ctx, err, "store retry guard record")
	}
	return nil
}

// prune deletes every record older than the staleness bound and every value
// that cannot be decoded, so the ledger cannot grow without bound and a
// corrupt entry cannot block a key forever.
func (g *retryGuard) prune(
	ctx context.Context,
	bucket libkv.Bucket,
	now libtime.DateTime,
) error {
	it := bucket.Iterator()
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		item := it.Item()
		var record retryGuardRecord
		decodeErr := item.Value(func(value []byte) error {
			return json.Unmarshal(value, &record)
		})
		if decodeErr == nil && now.Sub(record.PublishedAt).Duration() <= retryGuardStaleAfter {
			continue
		}
		if err := bucket.Delete(ctx, item.Key()); err != nil {
			return errors.Wrap(ctx, err, "prune retry guard record")
		}
	}
	return nil
}

func (g *retryGuard) readRecord(
	ctx context.Context,
	tx libkv.Tx,
	pr RetryGuardPR,
) (retryGuardRecord, bool, error) {
	bucket, err := tx.Bucket(ctx, retryGuardBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return retryGuardRecord{}, false, nil
		}
		return retryGuardRecord{}, false, errors.Wrap(ctx, err, "get retry guard bucket")
	}
	item, err := bucket.Get(ctx, retryGuardKey(pr))
	if err != nil {
		return retryGuardRecord{}, false, errors.Wrap(ctx, err, "get retry guard record")
	}
	if !item.Exists() {
		return retryGuardRecord{}, false, nil
	}
	var record retryGuardRecord
	if err := item.Value(func(value []byte) error {
		return json.Unmarshal(value, &record)
	}); err != nil {
		return retryGuardRecord{}, false, errors.Wrap(ctx, err, "decode retry guard record")
	}
	return record, true, nil
}

// recordAge returns the record's age, clamped to zero when the wall clock has
// jumped backwards. A future publish time must read fresh, never stale.
func (g *retryGuard) recordAge(publishedAt libtime.DateTime) libtime.Duration {
	age := g.currentDateTime.Now().Sub(publishedAt)
	if age.Duration() < 0 {
		return 0
	}
	return age
}

func retryGuardKey(pr RetryGuardPR) []byte {
	return []byte(fmt.Sprintf("%s/%s#%d@%s", pr.Owner, pr.Repo, pr.Number, pr.HeadSHA))
}
