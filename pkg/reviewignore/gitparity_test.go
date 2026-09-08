// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reviewignore_test

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bborbe/github-pr-watcher/pkg/reviewignore"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The size gate (this package) and the reviewer prompt (`/coding:pr-review`,
// which drives git's own ignore engine) must agree about every file: a path one
// side counts as reviewable and the other withholds is a silent inconsistency.
//
// Asserting hand-written expectations cannot catch that — it only proves the
// matcher does what the test author believed git does. This suite therefore
// asks git itself, via `check-ignore` against the same `.reviewignore`, and
// requires the two verdict sets to be identical.
var _ = Describe("git parity", func() {
	const reviewIgnore = `# generated / vendored
mocks/
*.snap

# dark-factory pipeline state
specs/
!specs/README.md
prompts/

!keep.snap
`

	paths := []string{
		"mocks/m.go",
		"a/b/c/mocks/deep.go",
		"fixture.snap",
		"test/data/deep.snap",
		"keep.snap",
		"specs/021.md",
		"specs/README.md",
		"prompts/completed/x.md",
		"pkg/real.go",
		"README.md",
	}

	var gitVerdict map[string]bool

	BeforeEach(func() {
		if _, err := exec.LookPath("git"); err != nil {
			Skip("git not in PATH — parity cannot be established")
		}
		dir := GinkgoT().TempDir()
		Expect(exec.Command("git", "init", "-q", dir).Run()).To(Succeed())

		ignorePath := filepath.Join(dir, ".reviewignore")
		Expect(os.WriteFile(ignorePath, []byte(reviewIgnore), 0o600)).To(Succeed())

		gitVerdict = map[string]bool{}
		for _, p := range paths {
			// #nosec G204 -- both interpolations are test-local and fixed:
			// ignorePath is under GinkgoT().TempDir(), and p comes from the
			// `paths` slice of string literals above. No external input
			// reaches this command.
			cmd := exec.Command(
				"git", "-c", "core.excludesFile="+ignorePath,
				"check-ignore", "--no-index", "-q", "--", p,
			)
			cmd.Dir = dir
			err := cmd.Run()
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				gitVerdict[p] = true // exit 0 → ignored
			case asExitError(err, &exitErr) && exitErr.ExitCode() == 1:
				gitVerdict[p] = false // exit 1 → not ignored
			default:
				Fail("git check-ignore failed for " + p + ": " + err.Error())
			}
		}
	})

	It("matches git's verdict for every path", func() {
		matcher := reviewignore.Parse([]byte(reviewIgnore))
		ours := map[string]bool{}
		for _, p := range paths {
			ours[p] = matcher.Match(p)
		}
		Expect(ours).To(Equal(gitVerdict))
	})

	It("covers both verdicts, so parity is not vacuous", func() {
		var ignored, kept int
		for _, v := range gitVerdict {
			if v {
				ignored++
			} else {
				kept++
			}
		}
		Expect(ignored).To(BeNumerically(">", 0))
		Expect(kept).To(BeNumerically(">", 0))
	})

	It("still refuses to exclude .reviewignore itself, where git would not care", func() {
		matcher := reviewignore.Parse([]byte(".reviewignore\n" + reviewIgnore))
		Expect(matcher.Match(reviewignore.Filename)).To(BeFalse())
	})
})

// asExitError reports whether err is an *exec.ExitError and stores it in target.
// Written out rather than using errors.As inline to keep the switch above
// readable.
func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}
