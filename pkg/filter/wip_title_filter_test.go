// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WIPTitleFilter", func() {
	var f filter.TaskCreationFilter

	BeforeEach(func() {
		f = filter.NewWIPTitleFilter()
	})

	It("skips a PR titled 'WIP: fix the thing'", func() {
		Expect(f.Skip(filter.PR{Title: "WIP: fix the thing"})).To(BeTrue())
	})
	It("skips a PR titled 'WIP fix the thing' (space separator)", func() {
		Expect(f.Skip(filter.PR{Title: "WIP fix the thing"})).To(BeTrue())
	})
	It("does not skip 'wip: foo' (lowercase)", func() {
		Expect(f.Skip(filter.PR{Title: "wip: foo"})).To(BeFalse())
	})
	It("does not skip 'Wip: foo' (mixed case)", func() {
		Expect(f.Skip(filter.PR{Title: "Wip: foo"})).To(BeFalse())
	})
	It("does not skip 'Working in progress'", func() {
		Expect(f.Skip(filter.PR{Title: "Working in progress"})).To(BeFalse())
	})
	It("does not skip 'feature: WIP cleanup' (WIP mid-title)", func() {
		Expect(f.Skip(filter.PR{Title: "feature: WIP cleanup"})).To(BeFalse())
	})
	It("does not skip 'WIPER: foo' (no separator after WIP)", func() {
		Expect(f.Skip(filter.PR{Title: "WIPER: foo"})).To(BeFalse())
	})
	It("does not skip an empty title", func() {
		Expect(f.Skip(filter.PR{Title: ""})).To(BeFalse())
	})
})
