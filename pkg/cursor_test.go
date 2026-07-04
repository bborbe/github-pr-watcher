// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/github-pr-watcher/pkg"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pkg.Cursor", func() {
	var (
		ctx       context.Context
		tmpDir    string
		startTime libtime.DateTime
	)

	BeforeEach(func() {
		ctx = context.Background()
		startTime = libtime.DateTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		var err error
		tmpDir, err = os.MkdirTemp("", "cursor-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	Describe("Load", func() {
		Context("file is missing", func() {
			It("returns cold-start state with startTime", func() {
				path := filepath.Join(tmpDir, "nonexistent.json")
				state, err := pkg.LoadCursor(ctx, path, startTime)
				Expect(err).NotTo(HaveOccurred())
				Expect(state.LastUpdatedAt).To(Equal(startTime))
				Expect(state.HeadSHAs).NotTo(BeNil())
				Expect(state.HeadSHAs).To(BeEmpty())
			})
		})

		Context("file has corrupt JSON", func() {
			It("returns cold-start state with startTime, no error", func() {
				path := filepath.Join(tmpDir, "corrupt.json")
				Expect(os.WriteFile(path, []byte("not-valid-json{"), 0600)).To(Succeed())
				state, err := pkg.LoadCursor(ctx, path, startTime)
				Expect(err).NotTo(HaveOccurred())
				Expect(state.LastUpdatedAt).To(Equal(startTime))
				Expect(state.HeadSHAs).NotTo(BeNil())
			})
		})

		Context("file has valid JSON", func() {
			It("returns correct state", func() {
				ts := libtime.DateTime(time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC))
				stored := pkg.Cursor{
					LastUpdatedAt: ts,
					HeadSHAs:      map[string]string{"abc": "sha1"},
				}
				path := filepath.Join(tmpDir, "valid.json")
				Expect(pkg.SaveCursor(ctx, path, stored)).To(Succeed())

				state, err := pkg.LoadCursor(ctx, path, startTime)
				Expect(err).NotTo(HaveOccurred())
				Expect(state.LastUpdatedAt).To(Equal(ts))
				Expect(state.HeadSHAs).To(Equal(map[string]string{"abc": "sha1"}))
			})
		})

		Context("file has nil HeadSHAs", func() {
			It("returns non-nil empty map", func() {
				path := filepath.Join(tmpDir, "nil-shas.json")
				Expect(
					os.WriteFile(path, []byte(`{"last_updated_at":"2026-01-01T00:00:00Z"}`), 0600),
				).To(Succeed())
				state, err := pkg.LoadCursor(ctx, path, startTime)
				Expect(err).NotTo(HaveOccurred())
				Expect(state.HeadSHAs).NotTo(BeNil())
			})
		})
	})

	Describe("Save", func() {
		Context("to unwritable directory", func() {
			It("returns error", func() {
				path := "/nonexistent-dir/cursor.json"
				state := pkg.Cursor{LastUpdatedAt: startTime, HeadSHAs: make(map[string]string)}
				err := pkg.SaveCursor(ctx, path, state)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("Save then Load round-trip", func() {
		It("preserves state", func() {
			path := filepath.Join(tmpDir, "roundtrip.json")
			ts := libtime.DateTime(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
			stored := pkg.Cursor{
				LastUpdatedAt: ts,
				HeadSHAs:      map[string]string{"key1": "sha-abc", "key2": "sha-def"},
			}
			Expect(pkg.SaveCursor(ctx, path, stored)).To(Succeed())

			loaded, err := pkg.LoadCursor(ctx, path, startTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.LastUpdatedAt).To(Equal(ts))
			Expect(loaded.HeadSHAs).To(Equal(stored.HeadSHAs))
		})
	})

	Describe("Load then Save preserves HeadSHAs", func() {
		It("existing entries are preserved", func() {
			path := filepath.Join(tmpDir, "preserve.json")
			ts := libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
			initial := pkg.Cursor{
				LastUpdatedAt: ts,
				HeadSHAs:      map[string]string{"pr-1": "sha-orig"},
			}
			Expect(pkg.SaveCursor(ctx, path, initial)).To(Succeed())

			loaded, err := pkg.LoadCursor(ctx, path, startTime)
			Expect(err).NotTo(HaveOccurred())
			loaded.HeadSHAs["pr-2"] = "sha-new"
			Expect(pkg.SaveCursor(ctx, path, loaded)).To(Succeed())

			final, err := pkg.LoadCursor(ctx, path, startTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(final.HeadSHAs).To(HaveKeyWithValue("pr-1", "sha-orig"))
			Expect(final.HeadSHAs).To(HaveKeyWithValue("pr-2", "sha-new"))
		})
	})
})
