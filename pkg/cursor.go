// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// DefaultCursorPath is the default path for cursor state persistence.
const DefaultCursorPath = "/data/cursor.json"

// Cursor holds the watcher's persisted poll state, including the last-seen update time and a map of task-identifier to head SHA for force-push detection.
type Cursor struct {
	LastUpdatedAt libtime.DateTime  `json:"last_updated_at"`
	HeadSHAs      map[string]string `json:"head_shas"`
}

// LoadCursor reads cursor state from path.
// Returns cold-start state with startTime if the file is missing or corrupt.
func LoadCursor(ctx context.Context, path string, startTime libtime.DateTime) (Cursor, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is config-controlled
	if err != nil {
		if os.IsNotExist(err) {
			glog.V(2).
				Infof("cursor file not found, using cold-start time=%s", startTime.Format(time.RFC3339))
			return Cursor{LastUpdatedAt: startTime, HeadSHAs: make(map[string]string)}, nil
		}
		return Cursor{}, errors.Wrap(ctx, err, "read cursor file")
	}
	var state Cursor
	if err := json.Unmarshal(data, &state); err != nil {
		glog.Warningf("cursor file corrupt, using cold-start path=%s err=%v", path, err)
		return Cursor{LastUpdatedAt: startTime, HeadSHAs: make(map[string]string)}, nil
	}
	if state.HeadSHAs == nil {
		state.HeadSHAs = make(map[string]string)
	}
	return state, nil
}

// SaveCursor persists cursor state to path atomically via a temp file + rename.
func SaveCursor(ctx context.Context, path string, state Cursor) error {
	data, err := json.Marshal(state)
	if err != nil {
		return errors.Wrap(ctx, err, "marshal cursor state")
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil { // #nosec G306 -- intentional 0600
		return errors.Wrap(ctx, err, "write cursor file")
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return errors.Wrap(ctx, err, "rename cursor file")
	}
	return nil
}
