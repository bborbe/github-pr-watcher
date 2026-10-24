// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "strings"

// ParseBotAllowlist splits a comma-separated allowlist string into a slice of trimmed, non-empty entries.
func ParseBotAllowlist(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// ParseTrustedAuthors splits a comma-separated trusted-authors string into a
// slice of trimmed, non-empty entries. Mirrors ParseBotAllowlist in behavior.
func ParseTrustedAuthors(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// ParseMechanicalOverrideAuthors splits a comma-separated
// MECHANICAL_OVERRIDE_AUTHORS string into trimmed, non-empty logins. The
// splitting rules are deliberately identical to ParseTrustedAuthors; the
// distinct name marks the distinct meaning — these logins may be approved
// without a review, which is stronger than "trusted" and must never be
// conflated with BOT_ALLOWLIST (skip) or TRUSTED_AUTHORS (may be reviewed).
func ParseMechanicalOverrideAuthors(raw string) []string {
	return ParseTrustedAuthors(raw)
}
