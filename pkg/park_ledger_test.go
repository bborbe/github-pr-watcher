// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/github-pr-watcher/pkg"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pkg.ParkLedger", func() {
	var (
		ctx    context.Context
		tmpDir string
		path   string
		now    libtime.DateTime
	)

	const key = "bborbe/repo#42"

	BeforeEach(func() {
		ctx = context.Background()
		now = libtime.DateTime(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
		var err error
		tmpDir, err = os.MkdirTemp("", "park-ledger-*")
		Expect(err).NotTo(HaveOccurred())
		path = filepath.Join(tmpDir, "park-ledger.json")
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	oneHourAgo := func() libtime.DateTime {
		return libtime.DateTime(now.Time().Add(-time.Hour))
	}

	writeLedger := func(entries map[string]pkg.ParkLedgerEntry) {
		data, err := json.Marshal(struct {
			Entries map[string]pkg.ParkLedgerEntry `json:"entries"`
		}{Entries: entries})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(path, data, 0600)).To(Succeed())
	}

	readEntries := func() map[string]pkg.ParkLedgerEntry {
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var file struct {
			Entries map[string]pkg.ParkLedgerEntry `json:"entries"`
		}
		Expect(json.Unmarshal(data, &file)).To(Succeed())
		return file.Entries
	}

	Describe("AC 5: the ledger, not the clock, decides the age", func() {
		It("case 1: same SHA and updatedAt now, start 3600s ago -> 3600", func() {
			writeLedger(map[string]pkg.ParkLedgerEntry{
				key: {Start: oneHourAgo(), SHA: "sha-a"},
			})
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: now},
			})

			Expect(starts).To(HaveLen(1))
			Expect(pkg.AgeSeconds(now, starts[0].Start)).To(BeNumerically("~", 3600, 60))
		})

		It("case 2: same SHA with updatedAt moved forward by a comment -> 3600", func() {
			writeLedger(map[string]pkg.ParkLedgerEntry{
				key: {Start: oneHourAgo(), SHA: "sha-a"},
			})
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: now},
			})

			Expect(starts).To(HaveLen(1))
			Expect(pkg.AgeSeconds(now, starts[0].Start)).To(BeNumerically("~", 3600, 60))
		})

		It("case 3: different SHA restarts the park at updatedAt -> 0", func() {
			writeLedger(map[string]pkg.ParkLedgerEntry{
				key: {Start: oneHourAgo(), SHA: "sha-a"},
			})
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-b", UpdatedAt: now},
			})

			Expect(starts).To(HaveLen(1))
			Expect(pkg.AgeSeconds(now, starts[0].Start)).To(BeNumerically("~", 0, 60))
		})

		It("case 4: no ledger entry seeds the start from updatedAt -> 3600", func() {
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: oneHourAgo()},
			})

			Expect(starts).To(HaveLen(1))
			Expect(pkg.AgeSeconds(now, starts[0].Start)).To(BeNumerically("~", 3600, 60))
		})
	})

	Describe("pruning", func() {
		It("drops entries absent from the scan", func() {
			writeLedger(map[string]pkg.ParkLedgerEntry{
				key:              {Start: oneHourAgo(), SHA: "sha-a"},
				"bborbe/other#7": {Start: oneHourAgo(), SHA: "sha-c"},
			})
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: now},
			})

			entries := readEntries()
			Expect(entries).To(HaveLen(1))
			Expect(entries).To(HaveKey(key))
			Expect(entries).NotTo(HaveKey("bborbe/other#7"))
		})
	})

	Describe("file shape", func() {
		It("round-trips the key with start and sha, mode 0600", func() {
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)

			ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: oneHourAgo()},
			})

			entries := readEntries()
			Expect(entries).To(HaveKey(key))
			Expect(entries[key].Start.IsZero()).To(BeFalse())
			Expect(entries[key].SHA).To(Equal("sha-a"))

			info, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})
	})

	Describe("missing file", func() {
		It("does not panic and seeds then writes the file", func() {
			ledger := pkg.NewParkLedger(path)
			Expect(func() { ledger.Read(ctx) }).NotTo(Panic())
			Expect(ledger.Has(key)).To(BeFalse())

			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: now},
			})

			Expect(starts).To(HaveLen(1))
			Expect(ledger.Has(key)).To(BeTrue())
			Expect(path).To(BeAnExistingFile())
			Expect(readEntries()).To(HaveKey(key))
		})
	})

	Describe("corrupt file", func() {
		It("keeps the in-memory entry and rewrites valid JSON", func() {
			ledger := pkg.NewParkLedger(path)
			ledger.Read(ctx)
			ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: oneHourAgo()},
			})

			Expect(os.WriteFile(path, []byte("{not json"), 0600)).To(Succeed())

			ledger.Read(ctx)
			starts := ledger.Reconcile(ctx, []pkg.ParkObservation{
				{Repo: "bborbe/repo", Number: 42, SHA: "sha-a", UpdatedAt: now},
			})

			Expect(starts).To(HaveLen(1))
			Expect(starts[0].Start.Equal(oneHourAgo())).To(BeTrue())
			Expect(readEntries()).To(HaveKey(key))
		})
	})
})
