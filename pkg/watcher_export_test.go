// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// Oversized re-exports the private oversized predicate for the external
// test package. The _test.go suffix keeps this file out of production
// builds. The park thresholds live behind the same predicate used by
// BuildCreateCommand, so the boundary semantics are tested once, here,
// rather than duplicated across routing tests.
var Oversized = oversized
