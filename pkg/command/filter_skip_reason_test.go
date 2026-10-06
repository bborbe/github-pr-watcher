// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The "filtered" log line in applyFilter is
// `trigger executor: filtered pr=%s/%s#%d reason=%s`, fed by
// command.FilterSkipReason (the re-exported private helper). glog output is
// not capturable in this repo's style, so these specs assert on the helper's
// return value directly — it is the exact string the log line receives as its
// fourth argument, so a regression in which filter is named fails here.
var _ = Describe("filterSkipReason (reason= field of the filtered log line)", func() {
	It("names the first voting member of a chain", func() {
		chain := filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewWIPTitleFilter(),
		}
		// Both members match; the first one wins.
		Expect(command.FilterSkipReason(chain, filter.PR{IsDraft: true, Title: "WIP: both"})).
			To(Equal("draft"))
	})

	It("names the later member when it is the only voter", func() {
		chain := filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewWIPTitleFilter(),
		}
		Expect(command.FilterSkipReason(chain, filter.PR{Title: "WIP: only wip"})).
			To(Equal("wip-title"))
	})

	It("falls back to the bare filter's own Name() when not a chain", func() {
		Expect(command.FilterSkipReason(filter.NewDraftFilter(), filter.PR{IsDraft: true})).
			To(Equal("draft"))
	})

	It("never panics on a nil filter", func() {
		Expect(command.FilterSkipReason(nil, filter.PR{})).To(Equal("unknown"))
	})
})
