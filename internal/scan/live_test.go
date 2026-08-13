// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveFilesystem(t *testing.T) {
	path := os.Getenv("INODE_COUNTER_LIVE_ROOT")
	if path == "" {
		t.Skip("set INODE_COUNTER_LIVE_ROOT to run a live filesystem scan")
	}

	roots, err := ResolveRoots([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	crossFilesystems := os.Getenv("INODE_COUNTER_CROSS_FILESYSTEMS") == "1"

	var final Event
	for event := range Start(ctx, roots, Options{
		Workers:          DefaultWorkers(),
		CrossFilesystems: crossFilesystems,
	}) {
		if event.Done {
			final = event
		}
	}
	if final.Cancelled {
		t.Fatalf("live scan of %s did not finish within one minute", path)
	}
	if final.Summary.Directories == 0 || final.Summary.Inodes == 0 {
		t.Fatalf("live scan of %s returned an empty summary: %+v", path, final.Summary)
	}
	t.Logf("live scan %s (cross=%t): %+v", path, crossFilesystems, final.Summary)
}
