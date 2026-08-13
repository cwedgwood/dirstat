// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// liveTimeout is generous by default because this test doubles as the
// measurement harness: under strace every syscall becomes a ptrace stop, so a
// tree that scans in half a minute unassisted will not finish in that budget.
func liveTimeout(t *testing.T) time.Duration {
	t.Helper()
	raw := os.Getenv("DIRSTAT_LIVE_TIMEOUT")
	if raw == "" {
		return 10 * time.Minute
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("DIRSTAT_LIVE_TIMEOUT=%q: %v", raw, err)
	}
	return timeout
}

func liveWorkers(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("DIRSTAT_WORKERS")
	if raw == "" {
		return DefaultWorkers()
	}
	workers, err := strconv.Atoi(raw)
	if err != nil || workers < 1 {
		t.Fatalf("DIRSTAT_WORKERS=%q: want a positive integer", raw)
	}
	return workers
}

func TestLiveFilesystem(t *testing.T) {
	path := os.Getenv("DIRSTAT_LIVE_ROOT")
	if path == "" {
		t.Skip("set DIRSTAT_LIVE_ROOT to run a live filesystem scan")
	}

	roots, err := ResolveRoots([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	timeout := liveTimeout(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	crossFilesystems := os.Getenv("DIRSTAT_CROSS_FILESYSTEMS") == "1"
	workers := liveWorkers(t)

	start := time.Now()
	var final Event
	for event := range Start(ctx, roots, Options{
		Workers:          workers,
		CrossFilesystems: crossFilesystems,
	}) {
		if event.Done {
			final = event
		}
	}
	elapsed := time.Since(start)

	if final.Cancelled {
		t.Fatalf(
			"live scan of %s did not finish within %s (scanned %d of %d directories); "+
				"raise DIRSTAT_LIVE_TIMEOUT",
			path,
			timeout,
			final.Progress.Scanned,
			final.Progress.Discovered,
		)
	}
	if final.Summary.Directories == 0 || final.Summary.Inodes == 0 {
		t.Fatalf("live scan of %s returned an empty summary: %+v", path, final.Summary)
	}

	t.Logf("live scan %s (cross=%t, workers=%d) in %s", path, crossFilesystems, workers, elapsed)
	t.Logf("  summary: %+v", final.Summary)
	// Skipped counts mount points that were not descended into. On ZFS every
	// child dataset is its own mount, so a non-zero value here means whole
	// subtrees are absent from the totals above.
	t.Logf(
		"  progress: discovered=%d scanned=%d skipped=%d errors=%d",
		final.Progress.Discovered,
		final.Progress.Scanned,
		final.Progress.Skipped,
		final.Progress.Errors,
	)
	if final.Progress.Skipped > 0 && !crossFilesystems {
		t.Logf(
			"  NOTE: %d mount points were not traversed; re-run with "+
				"DIRSTAT_CROSS_FILESYSTEMS=1 to include them",
			final.Progress.Skipped,
		)
	}
}
