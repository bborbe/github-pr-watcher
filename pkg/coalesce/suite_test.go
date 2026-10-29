// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package coalesce_test

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate

import (
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

func TestCoalesce(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	gomega.RegisterFailHandler(ginkgo.Fail)
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	ginkgo.RunSpecs(t, "Coalesce Suite", suiteConfig, reporterConfig)
}
