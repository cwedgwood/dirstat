// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"context"
	"fmt"
	"io/fs"
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

// walkExactMetrics computes the totals a scan must produce, independently of
// the scanner and of inventory.Accumulator.
func walkExactMetrics(t *testing.T, roots ...string) inventory.Metrics {
	t.Helper()

	metrics := inventory.Metrics{}
	seen := make(map[inventory.InodeKey]bool)
	charge := func(path string) {
		var stat syscall.Stat_t
		if err := syscall.Lstat(path, &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			metrics.Directories++
		} else {
			metrics.Files++
		}
		key := inventory.InodeKey{Device: uint64(stat.Dev), Inode: stat.Ino}
		if seen[key] {
			return
		}
		seen[key] = true
		metrics.Inodes++
		if stat.Blocks > 0 {
			metrics.Allocated += uint64(stat.Blocks) * 512
		}
		if stat.Size > 0 {
			metrics.Apparent += uint64(stat.Size)
		}
	}

	for _, root := range roots {
		charge(root)
		err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path != root {
				charge(path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return metrics
}

// makeChain creates a chain of nested directories so that its topmost
// directory stays unfinished for as long as the chain takes to walk.
func makeChain(t *testing.T, path string, depth int) {
	t.Helper()

	for range depth {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(path, "next")
	}
}

func collectEvents(t *testing.T, roots []Root, options Options) []Event {
	t.Helper()

	events := make([]Event, 0, 1024)
	for event := range Start(context.Background(), roots, options) {
		events = append(events, event)
	}
	return events
}

func TestAncestorsAccumulateBeforeSubtreesComplete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const branches = 4
	for index := range branches {
		branch := filepath.Join(root, fmt.Sprintf("branch-%d", index))
		makeChain(t, branch, 120)
		if err := os.WriteFile(filepath.Join(branch, "file"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	roots, err := ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, roots, Options{
		Workers:         2,
		refreshInterval: 100 * time.Microsecond,
	})

	provisional := 0
	sawBranchComplete := false
	earlyProvisional := false
	var final Event
	for _, event := range events {
		if event.Done {
			final = event
		}
		node := event.Node
		if node == nil {
			continue
		}
		if node.Path != root {
			if node.State == StateComplete && filepath.Dir(node.Path) == root {
				sawBranchComplete = true
			}
			continue
		}
		if node.State != StateScanning || node.Metrics.Files == 0 {
			continue
		}
		provisional++
		if !sawBranchComplete {
			earlyProvisional = true
		}
	}

	// The old child-count throttle refreshed a parent only after every 64th
	// completed child, so a four-child root received nothing at all here.
	if provisional == 0 {
		t.Fatal("root received no provisional roll-up while its subtrees were scanned")
	}
	if !earlyProvisional {
		t.Fatal("root roll-ups only appeared after a whole branch had completed")
	}
	if want := walkExactMetrics(t, root); final.Summary != want {
		t.Fatalf("summary = %+v, want %+v", final.Summary, want)
	}
}

func TestProvisionalRollUpOverCountsHardLinksThenSettles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	makeChain(t, first, 150)
	makeChain(t, second, 150)

	shared := filepath.Join(first, "shared")
	file, err := os.Create(shared)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse, so the duplicated apparent size dwarfs everything else in the
	// tree without writing megabytes.
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(shared, filepath.Join(second, "shared-link")); err != nil {
		t.Fatal(err)
	}

	roots, err := ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, roots, Options{
		Workers:         2,
		refreshInterval: 100 * time.Microsecond,
	})

	var rootFinal NodeUpdate
	overCounted := false
	peak := uint64(0)
	for _, event := range events {
		if event.Node == nil || event.Node.Path != root {
			continue
		}
		if event.Node.State == StateComplete {
			rootFinal = *event.Node
		}
		peak = max(peak, event.Node.Metrics.Apparent)
	}
	if rootFinal.Path == "" {
		t.Fatal("root never completed")
	}
	overCounted = peak > rootFinal.Metrics.Apparent
	if !overCounted {
		t.Fatalf(
			"provisional roll-up never exceeded the deduplicated total (peak %d, final %d)",
			peak,
			rootFinal.Metrics.Apparent,
		)
	}
	if want := walkExactMetrics(t, root); rootFinal.Metrics != want {
		t.Fatalf("root metrics = %+v, want %+v", rootFinal.Metrics, want)
	}
}

func TestFinalTotalsExactWithHardLinksAcrossSiblingsAndRoots(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	alpha := filepath.Join(parent, "alpha")
	beta := filepath.Join(parent, "beta")
	alphaInner := filepath.Join(alpha, "inner")
	betaInner := filepath.Join(beta, "inner")
	for _, path := range []string{alpha, beta, alphaInner, betaInner} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	shared := filepath.Join(alphaInner, "shared")
	if err := os.WriteFile(shared, make([]byte, 40960), 0o644); err != nil {
		t.Fatal(err)
	}
	// One inode reachable from a sibling directory, a sibling subtree, and a
	// second scan root.
	for _, link := range []string{
		filepath.Join(alpha, "sibling-link"),
		filepath.Join(betaInner, "cousin-link"),
		filepath.Join(beta, "root-link"),
	} {
		if err := os.Link(shared, link); err != nil {
			t.Fatal(err)
		}
	}

	singleRoot, err := ResolveRoots([]string{parent})
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[string]NodeUpdate)
	var final Event
	for _, event := range collectEvents(t, singleRoot, Options{Workers: 3}) {
		if event.Node != nil && event.Node.State == StateComplete {
			nodes[event.Node.Path] = *event.Node
		}
		if event.Done {
			final = event
		}
	}
	if want := walkExactMetrics(t, parent); final.Summary != want {
		t.Fatalf("single-root summary = %+v, want %+v", final.Summary, want)
	}
	for _, path := range []string{alpha, beta, alphaInner, betaInner} {
		if want := walkExactMetrics(t, path); nodes[path].Metrics != want {
			t.Fatalf("%s metrics = %+v, want %+v", path, nodes[path].Metrics, want)
		}
	}

	splitRoots, err := ResolveRoots([]string{alpha, beta})
	if err != nil {
		t.Fatal(err)
	}
	final = Event{}
	for _, event := range collectEvents(t, splitRoots, Options{Workers: 3}) {
		if event.Done {
			final = event
		}
	}
	if want := walkExactMetrics(t, alpha, beta); final.Summary != want {
		t.Fatalf("multi-root summary = %+v, want %+v", final.Summary, want)
	}
}
