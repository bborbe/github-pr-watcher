// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	"github.com/bborbe/github-pr-watcher/pkg"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("memDB", func() {
	libkv.IteratorTestSuite(libkv.ProviderFunc(func(ctx context.Context) (libkv.DB, error) {
		return pkg.NewMemDB(), nil
	}))
})
