// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cwedgwood/dirstat/internal/inventory"
)

func TestResolveRootsDefaultsToWorkingDirectory(t *testing.T) {
	root, err := ResolveRoots(nil)
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != 1 || root[0].Path != workingDirectory {
		t.Fatalf("roots = %+v, want %q", root, workingDirectory)
	}
}

func TestResolveRootsRejectsSymlinkAndOverlap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveRoots([]string{link}); err == nil {
		t.Fatal("ResolveRoots accepted a symbolic-link root")
	}
	nested := filepath.Join(link, "nested")
	if err := os.Mkdir(filepath.Join(child, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoots([]string{nested}); err == nil {
		t.Fatal("ResolveRoots accepted a symbolic link in an ancestor component")
	}
	if _, err := ResolveRoots([]string{root, child}); err == nil {
		t.Fatal("ResolveRoots accepted overlapping roots")
	}
}

func TestScanCountsEntriesAndUniqueInodes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}

	original := filepath.Join(first, "original")
	if err := os.WriteFile(original, []byte("shared content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(second, "hard-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, filepath.Join(first, "directory-link")); err != nil {
		t.Fatal(err)
	}
	sparse := filepath.Join(second, "sparse")
	file, err := os.Create(sparse)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(8 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	roots, err := ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	events := Start(context.Background(), roots, Options{Workers: 2})
	var final Event
	nodes := make(map[string]NodeUpdate)
	for event := range events {
		if event.Node != nil {
			nodes[event.Node.Path] = *event.Node
		}
		if event.Done {
			final = event
		}
	}

	if final.Cancelled {
		t.Fatal("scan was unexpectedly cancelled")
	}
	if final.Summary.Files != 4 {
		t.Fatalf("files = %d, want 4", final.Summary.Files)
	}
	if final.Summary.Directories != 3 {
		t.Fatalf("directories = %d, want 3", final.Summary.Directories)
	}
	if final.Summary.Inodes != 6 {
		t.Fatalf("inodes = %d, want 6", final.Summary.Inodes)
	}
	if final.Summary.Apparent <= 8<<20 {
		t.Fatalf("apparent size = %d, want sparse file plus other objects", final.Summary.Apparent)
	}
	if final.Summary.Allocated >= final.Summary.Apparent {
		t.Fatalf(
			"allocated size = %d, apparent size = %d; sparse file was not distinguished",
			final.Summary.Allocated,
			final.Summary.Apparent,
		)
	}

	firstNode := nodes[first]
	secondNode := nodes[second]
	if firstNode.Metrics.Inodes != 3 || secondNode.Metrics.Inodes != 3 {
		t.Fatalf(
			"subtree inode totals = %d and %d, want 3 and 3",
			firstNode.Metrics.Inodes,
			secondNode.Metrics.Inodes,
		)
	}
}

func TestScanDeduplicatesHardLinksAcrossRoots(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(first, "original")
	if err := os.WriteFile(original, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(second, "link")); err != nil {
		t.Fatal(err)
	}

	roots, err := ResolveRoots([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	var final Event
	for event := range Start(context.Background(), roots, Options{Workers: 2}) {
		if event.Done {
			final = event
		}
	}
	if final.Summary.Files != 2 ||
		final.Summary.Directories != 2 ||
		final.Summary.Inodes != 3 {
		t.Fatalf("unexpected multi-root summary: %+v", final.Summary)
	}
}

func TestRootDirectoryScanMarksMountPointsSkipped(t *testing.T) {
	info, err := os.Lstat("/")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := metadataFromInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	result := scanDirectory(
		context.Background(),
		directoryTask{path: "/", rootDevice: meta.key.Device},
		Options{},
	)

	foundDifferentFilesystem := false
	for _, child := range result.children {
		if child.meta.key.Device != meta.key.Device {
			foundDifferentFilesystem = true
			if !child.skipped {
				t.Fatalf("%s is on another filesystem but was not skipped", child.path)
			}
		}
	}
	if !foundDifferentFilesystem {
		t.Skip("root directory exposed no child mount points in this environment")
	}
}

func TestCancelledScanReportsCancellation(t *testing.T) {
	t.Parallel()

	roots, err := ResolveRoots([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := Start(ctx, roots, Options{Workers: 1})

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event channel closed without a cancellation event")
			}
			if event.Done && event.Cancelled {
				return
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for cancellation")
		}
	}
}

func TestMetadataAllocatedAndApparentSize(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sparse")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := metadataFromInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if meta.apparent != 16<<20 {
		t.Fatalf("apparent = %d, want %d", meta.apparent, 16<<20)
	}
	if meta.allocated >= meta.apparent {
		t.Fatalf("allocated = %d, want less than apparent %d", meta.allocated, meta.apparent)
	}
}

func TestScanIncludesFIFOWithoutFollowingIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	var final Event
	for event := range Start(context.Background(), roots, Options{Workers: 1}) {
		if event.Done {
			final = event
		}
	}
	if final.Summary.Files != 1 || final.Summary.Inodes != 2 {
		t.Fatalf("unexpected FIFO summary: %+v", final.Summary)
	}
}

func TestScanDirectoryReportsDisappearingDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "gone")
	result := scanDirectory(
		context.Background(),
		directoryTask{path: path},
		Options{},
	)
	if result.errorCount != 1 || result.accumulator.Metrics().Errors != 1 {
		t.Fatalf("unexpected missing-directory errors: %+v", result)
	}
	if len(result.errorSamples) != 1 {
		t.Fatalf("error samples = %d, want 1", len(result.errorSamples))
	}
}

func TestClassifyDirectoryDetectsAliases(t *testing.T) {
	t.Parallel()

	key := inventory.InodeKey{Device: 5, Inode: 7}
	visited := map[inventory.InodeKey]string{key: "/first"}
	state, existingPath := classifyDirectory(
		childDirectory{
			path: "/second",
			meta: metadata{key: key},
		},
		visited,
	)
	if state != StateAlias || existingPath != "/first" {
		t.Fatalf("classification = %q, %q; want alias of /first", state, existingPath)
	}
}

func TestClassifyDirectoryTracksSkippedMountAliases(t *testing.T) {
	t.Parallel()

	key := inventory.InodeKey{Device: 8, Inode: 9}
	visited := make(map[inventory.InodeKey]string)
	first := childDirectory{
		path:    "/mount/first",
		meta:    metadata{key: key},
		skipped: true,
	}
	state, _ := classifyDirectory(first, visited)
	if state != StateSkipped {
		t.Fatalf("first classification = %q, want skipped mount", state)
	}

	second := first
	second.path = "/mount/second"
	state, existingPath := classifyDirectory(second, visited)
	if state != StateAlias || existingPath != first.path {
		t.Fatalf(
			"second classification = %q, %q; want alias of %q",
			state,
			existingPath,
			first.path,
		)
	}
}

func TestScanDirectoryRefusesSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	result := scanDirectory(
		context.Background(),
		directoryTask{path: link},
		Options{CrossFilesystems: true},
	)
	if result.errorCount != 1 {
		t.Fatalf("error count = %d, want 1", result.errorCount)
	}
	if got := result.accumulator.Metrics(); got.Files != 0 || got.Directories != 0 {
		t.Fatalf("symlink target was inventoried: %+v", got)
	}
}

func TestScanDirectoryRejectsReplacementIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "queued")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := metadataFromInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "unexpected"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanDirectory(
		context.Background(),
		directoryTask{
			path:        path,
			rootDevice:  expected.key.Device,
			expected:    expected,
			hasExpected: true,
		},
		Options{},
	)
	got := result.accumulator.Metrics()
	if result.errorCount != 1 || got.Directories != 1 || got.Files != 0 {
		t.Fatalf("unexpected replacement result: errors=%d metrics=%+v", result.errorCount, got)
	}
}

func TestCancelledScanReportsCancellationWhenBufferIsFull(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for index := range 400 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("%03d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := Start(ctx, roots, Options{Workers: 1})

	deadline := time.Now().Add(2 * time.Second)
	for len(events) < cap(events) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(events) < cap(events) {
		cancel()
		t.Fatal("scanner event buffer did not fill")
	}
	cancel()

	foundCancelled := false
	for event := range events {
		if event.Done && event.Cancelled {
			foundCancelled = true
		}
	}
	if !foundCancelled {
		t.Fatal("full event buffer dropped terminal cancellation state")
	}
}
