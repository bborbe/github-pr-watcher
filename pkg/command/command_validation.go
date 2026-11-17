// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"

	"github.com/bborbe/maintainer/prurl"
)

// validatePRURLAndHeadSHA is the shared URL/HeadSHA rule for the commands that
// post a status or a review on a PR head. It lives in one function so the
// three commands cannot drift apart: URL must parse to a GitHub-platform PR
// URL, and HeadSHA must be non-empty.
func validatePRURLAndHeadSHA(ctx context.Context, url, headSHA string) error {
	return validation.All{
		validation.Name("URL", validation.HasValidationFunc(func(ctx context.Context) error {
			if url == "" {
				return errors.Wrap(ctx, validation.Error, "url must not be empty")
			}
			prInfo, err := prurl.ParsePRURL(ctx, url)
			if err != nil {
				return errors.Wrapf(ctx, err, "parse url %q", url)
			}
			if prInfo.Platform != prurl.PlatformGitHub {
				return errors.Errorf(
					ctx,
					"only github platform is supported, got %s",
					prInfo.Platform,
				)
			}
			return nil
		})),
		validation.Name("HeadSHA", validation.HasValidationFunc(func(ctx context.Context) error {
			if headSHA == "" {
				return errors.Wrap(ctx, validation.Error, "head_sha must not be empty")
			}
			return nil
		})),
	}.Validate(ctx)
}
