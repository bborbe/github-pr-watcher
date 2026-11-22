// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// parkLedgerIOTimeout bounds every ledger read and write. The scan must not
// block on ledger I/O, so a slow filesystem degrades the ledger, never the scan.
const parkLedgerIOTimeout = 2 * time.Second

// ParkLedgerEntry is one persisted park: when it started and the head SHA it
// started at. The SHA is what makes a comment harmless and a push a restart.
type ParkLedgerEntry struct {
	Start libtime.DateTime `json:"start"`
	SHA   string           `json:"sha"`
}

// ParkObservation is one PR this complete scan evaluated and found parked.
// UpdatedAt is the PR's search-result updated-at, the clock the scan already
// treats as reviewable-since.
type ParkObservation struct {
	Repo      string // "owner/repo"
	Number    int
	SHA       string
	UpdatedAt libtime.DateTime
}

// ParkedPRStart is a reconciled ledger entry with its identity restored, so
// the caller can publish an age without parsing the ledger key.
type ParkedPRStart struct {
	Repo   string
	Number int
	Start  libtime.DateTime
}

// ParkLedger persists the start of each currently-parked oversized PR.
type ParkLedger interface {
	// Read loads the file into memory. A missing file leaves the ledger
	// empty and logs at INFO; a corrupt file logs an error naming the path
	// and keeps the entries already in memory. It never returns an error:
	// ledger I/O must never fail a scan.
	Read(ctx context.Context)
	// Has reports whether key ("<owner>/<repo>#<pr>") is currently in the
	// in-memory ledger.
	Has(key string) bool
	// Reconcile seeds a new entry from the observation's UpdatedAt, keeps an
	// unchanged entry's Start, restarts a changed-SHA entry at UpdatedAt,
	// drops every entry absent from observations, persists the result
	// atomically, and returns the reconciled starts. A write failure is
	// logged and the in-memory result stays authoritative.
	Reconcile(ctx context.Context, observations []ParkObservation) []ParkedPRStart
}

// NewParkLedger returns a ParkLedger backed by the file at path.
func NewParkLedger(path string) ParkLedger {
	return &parkLedger{
		path:    path,
		entries: make(map[string]ParkLedgerEntry),
	}
}

type parkLedger struct {
	path    string
	mu      sync.Mutex
	entries map[string]ParkLedgerEntry
}

// parkLedgerFile is the persisted document shape:
// {"entries":{"<owner>/<repo>#<pr>":{"start":"<RFC3339>","sha":"<head sha>"}}}.
type parkLedgerFile struct {
	Entries map[string]ParkLedgerEntry `json:"entries"`
}

// Read implements ParkLedger.
func (l *parkLedger) Read(ctx context.Context) {
	data, err := parkLedgerIO(ctx, func() ([]byte, error) {
		return os.ReadFile(l.path) // #nosec G304 -- path is operator-supplied config
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			l.entries = make(map[string]ParkLedgerEntry)
			glog.Infof("park ledger not found, starting empty path=%s", l.path)
			return
		}
		glog.Errorf("park ledger read failed path=%s err=%v", l.path, err)
		return
	}
	var file parkLedgerFile
	if err := json.Unmarshal(data, &file); err != nil {
		glog.Errorf("park ledger corrupt path=%s err=%v", l.path, err)
		return
	}
	if file.Entries == nil {
		file.Entries = make(map[string]ParkLedgerEntry)
	}
	l.entries = file.Entries
}

// Has implements ParkLedger.
func (l *parkLedger) Has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.entries[key]
	return ok
}

// Reconcile implements ParkLedger.
func (l *parkLedger) Reconcile(
	ctx context.Context,
	observations []ParkObservation,
) []ParkedPRStart {
	l.mu.Lock()
	defer l.mu.Unlock()

	next := make(map[string]ParkLedgerEntry, len(observations))
	starts := make([]ParkedPRStart, 0, len(observations))
	for _, obs := range observations {
		key := fmt.Sprintf("%s#%d", obs.Repo, obs.Number)
		start := obs.UpdatedAt
		// Freeze the start on an unchanged SHA, so a comment that moves
		// updated-at cannot reset the age; a changed SHA restarts at UpdatedAt.
		if existing, ok := l.entries[key]; ok && existing.SHA == obs.SHA {
			start = existing.Start
		}
		next[key] = ParkLedgerEntry{Start: start, SHA: obs.SHA}
		starts = append(starts, ParkedPRStart{
			Repo:   obs.Repo,
			Number: obs.Number,
			Start:  start,
		})
	}
	// Replacing the map prunes every entry absent from this scan's observations.
	l.entries = next
	l.persist(ctx, next)
	return starts
}

// persist writes entries atomically via a temp file + rename. A marshal, write
// or rename failure is logged and swallowed: the in-memory ledger is
// authoritative and the next scan rewrites the file.
func (l *parkLedger) persist(ctx context.Context, entries map[string]ParkLedgerEntry) {
	data, err := json.Marshal(parkLedgerFile{Entries: entries})
	if err != nil {
		glog.Errorf("park ledger marshal failed path=%s err=%v", l.path, err)
		return
	}
	if _, err := parkLedgerIO(ctx, func() ([]byte, error) {
		// #nosec G306 -- intentional 0600
		return nil, os.WriteFile(l.path+".tmp", data, 0600)
	}); err != nil {
		glog.Errorf("park ledger write failed path=%s err=%v", l.path, err)
		return
	}
	if err := os.Rename(l.path+".tmp", l.path); err != nil {
		glog.Errorf("park ledger rename failed path=%s err=%v", l.path, err)
	}
}

// parkLedgerIO runs op in a goroutine bounded by parkLedgerIOTimeout so a slow
// filesystem cannot delay the scan. os.ReadFile/os.WriteFile cannot be
// cancelled, so the result channel is buffered: a timed-out operation finishes
// on its own instead of blocking forever.
func parkLedgerIO(ctx context.Context, op func() ([]byte, error)) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := op()
		done <- result{data: data, err: err}
	}()
	timeoutCtx, cancel := context.WithTimeout(ctx, parkLedgerIOTimeout)
	defer cancel()
	select {
	case res := <-done:
		return res.data, res.err
	case <-timeoutCtx.Done():
		glog.Errorf(
			"park ledger io timeout path timeout=%s err=%v",
			parkLedgerIOTimeout,
			timeoutCtx.Err(),
		)
		return nil, errors.Wrap(ctx, timeoutCtx.Err(), "park ledger io timeout")
	}
}

// AgeSeconds reports how many seconds have elapsed from start to now,
// clamped at 0 so a host clock skew never publishes a negative age.
func AgeSeconds(now, start libtime.DateTime) float64 {
	seconds := now.Sub(start).Duration().Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}
