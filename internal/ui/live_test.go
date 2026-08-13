// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cwedgwood/dirstat/internal/scan"
)

func TestLiveUI(t *testing.T) {
	path := os.Getenv("DIRSTAT_UI_LIVE_ROOT")
	if path == "" {
		t.Skip("set DIRSTAT_UI_LIVE_ROOT to run a live UI-model scan")
	}

	roots, err := scan.ResolveRoots([]string{path})
	if err != nil {
		t.Fatal(err)
	}

	timeout := 10 * time.Minute
	if raw := os.Getenv("DIRSTAT_LIVE_TIMEOUT"); raw != "" {
		timeout, err = time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("DIRSTAT_LIVE_TIMEOUT=%q: %v", raw, err)
		}
	}
	workers := scan.DefaultWorkers()
	if raw := os.Getenv("DIRSTAT_WORKERS"); raw != "" {
		workers, err = strconv.Atoi(raw)
		if err != nil || workers < 1 {
			t.Fatalf("DIRSTAT_WORKERS=%q: want a positive integer", raw)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	model := New(
		ctx,
		roots,
		scan.Options{
			Workers:          workers,
			CrossFilesystems: os.Getenv("DIRSTAT_CROSS_FILESYSTEMS") == "1",
		},
		Options{NoColor: true},
	)
	defer model.Close()

	start := time.Now()
	command := model.Init()
	batches := 0
	for !model.done {
		if command == nil {
			t.Fatal("UI event loop stopped before scan completion")
		}
		message := command()
		_, command = model.Update(message)
		_ = model.View()
		batches++
	}
	elapsed := time.Since(start)

	if model.cancelled {
		t.Fatalf(
			"live UI scan of %s did not finish within %s (scanned %d of %d directories); "+
				"raise DIRSTAT_LIVE_TIMEOUT",
			path,
			timeout,
			model.progress.Scanned,
			model.progress.Discovered,
		)
	}
	t.Logf(
		"live UI scan %s (workers=%d): %d directories, %d skipped, %d batches, %s",
		path,
		workers,
		model.progress.Scanned,
		model.progress.Skipped,
		batches,
		elapsed,
	)
}
