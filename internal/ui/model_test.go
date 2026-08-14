// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/scan"
)

func TestApplyEventBuildsAndSortsTree(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:    "/root",
		Path:  "/root",
		Name:  "root",
		Root:  true,
		State: scan.StateScanning,
	}})
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:       "/root/small",
		ParentID: "/root",
		Path:     "/root/small",
		Name:     "small",
		State:    scan.StateComplete,
		Metrics:  inventory.Metrics{Allocated: 1},
	}})
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:       "/root/large",
		ParentID: "/root",
		Path:     "/root/large",
		Name:     "large",
		State:    scan.StateComplete,
		Metrics:  inventory.Metrics{Allocated: 100},
	}})

	rows := model.visibleRows()
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if rows[1].id != "/root/large" || rows[2].id != "/root/small" {
		t.Fatalf("children not sorted by allocated bytes: %+v", rows)
	}
}

func TestFilterRetainsAncestors(t *testing.T) {
	t.Parallel()

	model := testModel()
	for _, update := range []scan.NodeUpdate{
		{ID: "/root", Path: "/root", Name: "root", Root: true},
		{ID: "/root/parent", ParentID: "/root", Path: "/root/parent", Name: "parent"},
		{
			ID:       "/root/parent/needle",
			ParentID: "/root/parent",
			Path:     "/root/parent/needle",
			Name:     "needle",
		},
		{ID: "/root/other", ParentID: "/root", Path: "/root/other", Name: "other"},
	} {
		model.applyEvent(scan.Event{Node: &update})
	}
	model.filter = "needle"

	rows := model.visibleRows()
	if len(rows) != 3 {
		t.Fatalf("filtered rows = %d, want matching node and two ancestors", len(rows))
	}
	if rows[0].id != "/root" ||
		rows[1].id != "/root/parent" ||
		rows[2].id != "/root/parent/needle" {
		t.Fatalf("unexpected filtered rows: %+v", rows)
	}
}

func TestKeyHandlingExpandsAndReversesSort(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:   "/root",
		Path: "/root",
		Name: "root",
		Root: true,
	}})
	model.expanded["/root"] = false

	if _, command := model.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); command != nil {
		t.Fatal("enter unexpectedly returned a command")
	}
	if !model.expanded["/root"] {
		t.Fatal("enter did not expand selected directory")
	}

	wasDescending := model.descending
	model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	if model.descending == wasDescending {
		t.Fatal("uppercase O did not reverse sorting")
	}
}

func TestScannerUpdatePreservesSelectedDirectory(t *testing.T) {
	t.Parallel()

	model := testModel()
	for _, update := range []scan.NodeUpdate{
		{ID: "/root", Path: "/root", Name: "root", Root: true},
		{
			ID:       "/root/first",
			ParentID: "/root",
			Path:     "/root/first",
			Name:     "first",
			Metrics:  inventory.Metrics{Allocated: 100},
		},
		{
			ID:       "/root/second",
			ParentID: "/root",
			Path:     "/root/second",
			Name:     "second",
			Metrics:  inventory.Metrics{Allocated: 1},
		},
	} {
		model.applyEvent(scan.Event{Node: &update})
	}
	model.cursor = 1
	rows := model.visibleRows()
	selectedID := model.selectedID(rows)

	update := scan.NodeUpdate{
		ID:       "/root/second",
		ParentID: "/root",
		Path:     "/root/second",
		Name:     "second",
		Metrics:  inventory.Metrics{Allocated: 1000},
	}
	model.applyEvent(scan.Event{Node: &update})
	model.restoreSelection(model.visibleRows(), selectedID)
	if got := model.selectedID(model.visibleRows()); got != selectedID {
		t.Fatalf("selection changed from %q to %q after resort", selectedID, got)
	}
}

func TestSortPickerSelectsInodes(t *testing.T) {
	t.Parallel()

	model := testModel()
	for _, update := range []scan.NodeUpdate{
		{ID: "/root", Path: "/root", Name: "root", Root: true},
		{
			ID:       "/root/space",
			ParentID: "/root",
			Path:     "/root/space",
			Name:     "space",
			Metrics:  inventory.Metrics{Allocated: 100, Inodes: 1},
		},
		{
			ID:       "/root/inodes",
			ParentID: "/root",
			Path:     "/root/inodes",
			Name:     "inodes",
			Metrics:  inventory.Metrics{Allocated: 1, Inodes: 100},
		},
	} {
		model.applyEvent(scan.Event{Node: &update})
	}

	model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !model.selectSort {
		t.Fatal("s did not open the sort picker")
	}
	model.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	model.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if model.sortField != inventory.SortInodes || model.selectSort {
		t.Fatalf("sort selection = %v, open = %t; want inodes and closed", model.sortField, model.selectSort)
	}
	rows := model.visibleRows()
	if rows[1].id != "/root/inodes" {
		t.Fatalf("inode-heavy directory was not sorted first: %+v", rows)
	}
}

func TestSortOptionSeedsFieldAndDirection(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		field      inventory.SortField
		descending bool
	}{
		{field: inventory.SortAllocated, descending: true},
		{field: inventory.SortInodes, descending: true},
		{field: inventory.SortFiles, descending: true},
		{field: inventory.SortApparent, descending: true},
		{field: inventory.SortName, descending: false},
	} {
		model := New(
			context.Background(),
			nil,
			scan.Options{Workers: 1},
			Options{NoColor: true, Sort: testCase.field},
		)
		if model.sortField != testCase.field || model.descending != testCase.descending {
			t.Errorf(
				"--sort %s opened as %s descending=%t, want %s descending=%t",
				testCase.field,
				model.sortField,
				model.descending,
				testCase.field,
				testCase.descending,
			)
		}
		model.Close()
	}
}

func TestSortDirectionFollowsFieldAcrossNameBoundary(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.applySortChoice(inventory.SortName)
	if model.descending {
		t.Fatal("choosing name kept the descending direction of a quantity")
	}
	model.applySortChoice(inventory.SortInodes)
	if !model.descending {
		t.Fatal("returning to a quantity kept the ascending direction of name")
	}

	// Only a change of kind resets it; O still reverses either.
	model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	model.applySortChoice(inventory.SortFiles)
	if model.descending {
		t.Fatal("switching between two quantities discarded the reversal")
	}
}

func TestTiesBreakAlphabeticallyInBothDirections(t *testing.T) {
	t.Parallel()

	model := testModel()
	for _, update := range []scan.NodeUpdate{
		{ID: "/root", Path: "/root", Name: "root", Root: true},
		{ID: "/root/zulu", ParentID: "/root", Path: "/root/zulu", Name: "zulu"},
		{ID: "/root/alpha", ParentID: "/root", Path: "/root/alpha", Name: "alpha"},
	} {
		model.applyEvent(scan.Event{Node: &update})
	}

	for _, descending := range []bool{true, false} {
		model.descending = descending
		rows := model.visibleRows()
		if rows[1].id != "/root/alpha" || rows[2].id != "/root/zulu" {
			t.Fatalf("descending=%t reordered rows equal under the metric: %+v", descending, rows)
		}
	}

	// Sorting by name is the one case where the direction owns the order.
	model.applySortChoice(inventory.SortName)
	rows := model.visibleRows()
	if rows[1].id != "/root/alpha" {
		t.Fatalf("name sorting did not open alphabetically: %+v", rows)
	}
	model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	if rows := model.visibleRows(); rows[1].id != "/root/zulu" {
		t.Fatalf("reversed name sorting did not start at Z: %+v", rows)
	}
}

func TestPageUpAndPageDownMoveByViewport(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.height = 12
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:   "/root",
		Path: "/root",
		Name: "root",
		Root: true,
	}})
	for index := range 30 {
		update := scan.NodeUpdate{
			ID:       fmt.Sprintf("/root/%02d", index),
			ParentID: "/root",
			Path:     fmt.Sprintf("/root/%02d", index),
			Name:     fmt.Sprintf("%02d", index),
		}
		model.applyEvent(scan.Event{Node: &update})
	}
	rows := model.visibleRows()
	step := model.pageStep(rows)

	model.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if model.cursor != step {
		t.Fatalf("page down cursor = %d, want %d", model.cursor, step)
	}
	model.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if model.cursor != 0 {
		t.Fatalf("page up cursor = %d, want 0", model.cursor)
	}
}

func TestScanEventBatchDrainsBufferedEvents(t *testing.T) {
	t.Parallel()

	model := testModel()
	events := make(chan scan.Event, 4)
	model.events = events
	events <- scan.Event{Node: &scan.NodeUpdate{
		ID:       "/root/child",
		ParentID: "/root",
		Path:     "/root/child",
		Name:     "child",
		State:    scan.StateComplete,
	}}
	events <- scan.Event{Done: true, Summary: inventory.Metrics{Directories: 2}}

	first := scan.Event{Node: &scan.NodeUpdate{
		ID:    "/root",
		Path:  "/root",
		Name:  "root",
		Root:  true,
		State: scan.StateScanning,
	}}
	_, command := model.Update(scanEventMsg{
		event:   first,
		channel: events,
		ok:      true,
	})
	if command != nil {
		t.Fatal("completed buffered batch scheduled another scanner wait")
	}
	if !model.done || len(model.nodes) != 2 || model.summary.Directories != 2 {
		t.Fatalf(
			"buffered events were not applied together: done=%t nodes=%d summary=%+v",
			model.done,
			len(model.nodes),
			model.summary,
		)
	}
}

// scanningModel returns a model already showing a root, with an empty event
// channel so that a batch drains immediately.
func scanningModel(t *testing.T) (*Model, chan scan.Event) {
	t.Helper()

	model := testModel()
	model.width = 120
	model.height = 24
	events := make(chan scan.Event, 4)
	model.events = events
	model.Update(scanEventMsg{
		event: scan.Event{Node: &scan.NodeUpdate{
			ID:    "/root",
			Path:  "/root",
			Name:  "root",
			Root:  true,
			State: scan.StateScanning,
		}},
		channel: events,
		ok:      true,
	})
	model.View()
	return model, events
}

func scanUpdate(model *Model, events chan scan.Event, files uint64) {
	model.Update(scanEventMsg{
		event: scan.Event{
			Node: &scan.NodeUpdate{
				ID:      "/root",
				Path:    "/root",
				Name:    "root",
				Root:    true,
				State:   scan.StateScanning,
				Metrics: inventory.Metrics{Files: files},
			},
			Progress: scan.Progress{Scanned: files},
		},
		channel: events,
		ok:      true,
	})
}

func TestScanBatchesShareOneFrame(t *testing.T) {
	t.Parallel()

	model, events := scanningModel(t)
	builds := model.frameBuilds
	for files := uint64(1); files <= 20; files++ {
		scanUpdate(model, events, files)
		model.View()
	}
	if model.frameBuilds != builds {
		t.Fatalf("scanner batches rebuilt the display %d times, want 0", model.frameBuilds-builds)
	}
	if model.nodes["/root"].metrics.Files != 20 {
		t.Fatal("scanner updates were not applied while frames were reused")
	}
}

func TestKeyInputRebuildsTheFrameImmediately(t *testing.T) {
	t.Parallel()

	model, events := scanningModel(t)
	before := model.frame
	scanUpdate(model, events, 42)
	if model.View() != before {
		t.Fatal("a scanner batch rebuilt the display instead of deferring it")
	}

	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	frame := model.View()
	if !strings.Contains(frame, "42") {
		t.Fatalf("key input did not rebuild the display: %q", frame)
	}
}

func TestFinalFrameShowsFinalTotals(t *testing.T) {
	t.Parallel()

	model, events := scanningModel(t)
	scanUpdate(model, events, 7)
	model.View()

	model.Update(scanEventMsg{
		event: scan.Event{
			Node: &scan.NodeUpdate{
				ID:      "/root",
				Path:    "/root",
				Name:    "root",
				Root:    true,
				State:   scan.StateComplete,
				Metrics: inventory.Metrics{Files: 99},
			},
			Progress: scan.Progress{Scanned: 99, Discovered: 99},
			Done:     true,
			Summary:  inventory.Metrics{Files: 99},
		},
		channel: events,
		ok:      true,
	})
	frame := model.View()
	if !strings.Contains(frame, "complete") || !strings.Contains(frame, "99") {
		t.Fatalf("final frame is not the final state: %q", frame)
	}
}

func TestFilterCtrlCQuits(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.filtering = true
	_, command := model.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if command == nil {
		t.Fatal("Ctrl+C in the filter prompt did not quit")
	}
}

func TestRenderRowShowsUpdatingStatusBeforeFiles(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.width = 120
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:    "/root",
		Path:  "/root",
		Name:  "root",
		Root:  true,
		State: scan.StateScanning,
		Metrics: inventory.Metrics{
			Files: 42,
		},
	}})

	line := model.renderRow(row{id: "/root"}, model.width)
	statusIndex := strings.Index(line, "updating...")
	filesIndex := strings.Index(line, "42")
	if statusIndex < 0 || filesIndex < 0 || statusIndex >= filesIndex {
		t.Fatalf("updating status is not before files column: %q", line)
	}

	model.nodes["/root"].state = scan.StateComplete
	if line := model.renderRow(row{id: "/root"}, model.width); strings.Contains(line, "updating...") {
		t.Fatalf("completed row still shows updating status: %q", line)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	if got := truncate("abcdef", 5); got != "ab..." {
		t.Fatalf("truncate = %q", got)
	}
}

func TestFormatElapsedStaysReadableAcrossTheWholeRange(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 0, want: "0ms"},
		{elapsed: 41 * time.Millisecond, want: "41ms"},
		{elapsed: 999 * time.Millisecond, want: "999ms"},
		{elapsed: 4190 * time.Millisecond, want: "4.19s"},
		{elapsed: 59 * time.Second, want: "59.00s"},
		// The boundary the sub-minute branch exists to guard: printing to
		// hundredths rounds these up to sixty seconds, which is a minute.
		{elapsed: 59994 * time.Millisecond, want: "59.99s"},
		{elapsed: 59999 * time.Millisecond, want: "1m00s"},
		{elapsed: 60 * time.Second, want: "1m00s"},
		{elapsed: 3599 * time.Second, want: "59m59s"},
		{elapsed: 3599999 * time.Millisecond, want: "1h00m00s"},
		{elapsed: 3600 * time.Second, want: "1h00m00s"},
		{elapsed: 7325 * time.Second, want: "2h02m05s"},
	} {
		if got := formatElapsed(testCase.elapsed); got != testCase.want {
			t.Errorf("formatElapsed(%s) = %q, want %q", testCase.elapsed, got, testCase.want)
		}
	}
}

// benchmarkTreeModel builds a tree whose shape and path lengths resemble a real
// home directory: a deep prefix so every identifier is a long absolute path, a
// wide directory holding several thousand siblings, and enough mid-level
// directories to reach tens of thousands of nodes. Everything is expanded, so
// visibleRows walks and orders every sibling group.
func benchmarkTreeModel(midLevel int, midChildren int, wide int) *Model {
	model := testModel()
	const prefix = "/home/benchmark/workspace/checkouts/deep-directory-tree"

	apply := func(id string, parentID string, name string, root bool, allocated uint64) {
		update := scan.NodeUpdate{
			ID:       id,
			ParentID: parentID,
			Path:     id,
			Name:     name,
			Root:     root,
			State:    scan.StateComplete,
			Metrics:  inventory.Metrics{Allocated: allocated, Inodes: allocated, Files: allocated},
		}
		model.applyEvent(scan.Event{Node: &update})
	}

	apply(prefix, "", "deep-directory-tree", true, 0)
	wideID := prefix + "/wide-sibling-directory"
	apply(wideID, prefix, "wide-sibling-directory", false, uint64(wide))
	model.expanded[wideID] = true
	for index := range wide {
		name := fmt.Sprintf("sibling-directory-%06d", index)
		apply(wideID+"/"+name, wideID, name, false, uint64(index%997))
	}
	for parentIndex := range midLevel {
		parentName := fmt.Sprintf("project-directory-%04d", parentIndex)
		parentID := prefix + "/" + parentName
		apply(parentID, prefix, parentName, false, uint64(parentIndex))
		model.expanded[parentID] = true
		for childIndex := range midChildren {
			childName := fmt.Sprintf("nested-source-directory-%05d", childIndex)
			apply(parentID+"/"+childName, parentID, childName, false, uint64(childIndex%503))
		}
	}
	return model
}

func BenchmarkVisibleRowsExpanded(b *testing.B) {
	model := benchmarkTreeModel(60, 500, 4_000)
	want := len(model.nodes)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows := model.visibleRows()
		if len(rows) != want {
			b.Fatalf("visible rows = %d, want %d", len(rows), want)
		}
	}
}

func BenchmarkVisibleRowsCollapsed(b *testing.B) {
	model := testModel()
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:   "/root",
		Path: "/root",
		Name: "root",
		Root: true,
	}})
	model.expanded["/root"] = false
	for index := range 100_000 {
		id := fmt.Sprintf("/root/%06d", index)
		model.nodes[id] = &treeNode{
			id:        id,
			parentID:  "/root",
			path:      id,
			name:      id,
			lowerName: id,
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows := model.visibleRows()
		if len(rows) != 1 {
			b.Fatalf("visible rows = %d, want 1", len(rows))
		}
	}
}

func BenchmarkScanEventBatch(b *testing.B) {
	model := testModel()
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:   "/root",
		Path: "/root",
		Name: "root",
		Root: true,
	}})
	for index := range 5_000 {
		id := fmt.Sprintf("/root/%05d", index)
		update := scan.NodeUpdate{
			ID:       id,
			ParentID: "/root",
			Path:     id,
			Name:     fmt.Sprintf("%05d", index),
			Metrics:  inventory.Metrics{Allocated: uint64(index)},
		}
		model.applyEvent(scan.Event{Node: &update})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		events := make(chan scan.Event, 256)
		for index := range 256 {
			id := fmt.Sprintf("/root/%05d", index)
			events <- scan.Event{Node: &scan.NodeUpdate{
				ID:       id,
				ParentID: "/root",
				Path:     id,
				Name:     fmt.Sprintf("%05d", index),
				Metrics:  inventory.Metrics{Allocated: uint64(10_000 + index)},
			}}
		}
		first := <-events
		model.events = events
		model.Update(scanEventMsg{event: first, channel: events, ok: true})
	}
}

func testModel() *Model {
	context, cancel := context.WithCancel(context.Background())
	cancel()
	return &Model{
		baseContext: context,
		nodes:       make(map[string]*treeNode),
		expanded:    make(map[string]bool),
		sortField:   inventory.SortAllocated,
		descending:  true,
	}
}

func TestProvisionalParentRowKeepsUpdatingStatusAndSettlesDown(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.width = 120
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:      "/root",
		Path:    "/root",
		Name:    "root",
		Root:    true,
		State:   scan.StateScanning,
		Metrics: inventory.Metrics{Files: 7, Inodes: 9},
	}})
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:       "/root/child",
		ParentID: "/root",
		Path:     "/root/child",
		Name:     "child",
		State:    scan.StateScanning,
	}})

	// A parent carrying provisional descendant totals is rendered as an
	// expandable row, so the status column is the only thing telling the user
	// the number is still moving.
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:      "/root",
		Path:    "/root",
		Name:    "root",
		Root:    true,
		State:   scan.StateScanning,
		Metrics: inventory.Metrics{Files: 5000, Inodes: 6000},
	}})
	line := model.renderRow(row{id: "/root"}, model.width)
	if !strings.Contains(line, "updating...") || !strings.Contains(line, "5.0K") {
		t.Fatalf("provisional parent row is not marked as still updating: %q", line)
	}

	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:      "/root",
		Path:    "/root",
		Name:    "root",
		Root:    true,
		State:   scan.StateComplete,
		Metrics: inventory.Metrics{Files: 5000, Inodes: 4000},
	}})
	line = model.renderRow(row{id: "/root"}, model.width)
	if strings.Contains(line, "updating...") || !strings.Contains(line, "4.0K") {
		t.Fatalf("settled parent row did not adopt the deduplicated total: %q", line)
	}
}

func nodeEvent(path string, parent string, allocated uint64) scan.Event {
	name := path[strings.LastIndex(path, "/")+1:]
	return scan.Event{Node: &scan.NodeUpdate{
		ID:       path,
		ParentID: parent,
		Path:     path,
		Name:     name,
		Root:     parent == "",
		State:    scan.StateComplete,
		Metrics:  inventory.Metrics{Allocated: allocated},
	}}
}

// feedEvents drives one batch through Update the way the Bubble Tea loop does,
// so the incremental arrival of nodes is exercised rather than bypassed.
func feedEvents(t *testing.T, model *Model, events ...scan.Event) {
	t.Helper()

	channel := make(chan scan.Event, len(events))
	for _, event := range events[1:] {
		channel <- event
	}
	model.events = channel
	model.Update(scanEventMsg{event: events[0], channel: channel, ok: true})
}

func buildTree(t *testing.T, model *Model) {
	t.Helper()

	feedEvents(t, model,
		nodeEvent("/root", "", 300),
		nodeEvent("/root/keep", "/root", 200),
		nodeEvent("/root/keep/deep", "/root/keep", 100),
		nodeEvent("/root/other", "/root", 1),
	)
}

func selectPath(t *testing.T, model *Model, path string) {
	t.Helper()

	for index, item := range model.visibleRows() {
		if item.id == path {
			model.cursor = index
			return
		}
	}
	t.Fatalf("%s is not a visible row", path)
}

func TestRescanRestoresSelectionAndExpansion(t *testing.T) {
	t.Parallel()

	model := testModel()
	buildTree(t, model)
	model.expanded["/root/keep"] = true
	selectPath(t, model, "/root/keep/deep")

	model.restart()
	if model.cursor != 0 {
		t.Fatalf("cursor = %d immediately after restart, want the top of the empty tree", model.cursor)
	}

	// The target cannot exist when the root arrives, so the restore has to
	// survive until its own event turns up.
	feedEvents(t, model, nodeEvent("/root", "", 300))
	if got := model.selectedID(model.visibleRows()); got != "/root" {
		t.Fatalf("selection = %q before the target reappeared, want the root", got)
	}
	feedEvents(t, model,
		nodeEvent("/root/keep", "/root", 200),
		nodeEvent("/root/other", "/root", 1),
		nodeEvent("/root/keep/deep", "/root/keep", 100),
	)

	if got := model.selectedID(model.visibleRows()); got != "/root/keep/deep" {
		t.Fatalf("selection after rescan = %q, want /root/keep/deep", got)
	}
	if !model.expanded["/root/keep"] {
		t.Fatal("expansion of /root/keep was not restored")
	}

	// A restored deep cursor must be scrolled into view, not left below the
	// visible window.
	model.offset = 0
	model.adjustOffset(len(model.visibleRows()), 2)
	if model.cursor < model.offset || model.cursor >= model.offset+2 {
		t.Fatalf("restored cursor %d is outside the visible window at offset %d", model.cursor, model.offset)
	}
}

func TestRescanFallsBackToSurvivingAncestor(t *testing.T) {
	t.Parallel()

	model := testModel()
	buildTree(t, model)
	model.expanded["/root/keep"] = true
	selectPath(t, model, "/root/keep/deep")

	model.restart()
	feedEvents(t, model,
		nodeEvent("/root", "", 200),
		nodeEvent("/root/other", "/root", 1),
		nodeEvent("/root/keep", "/root", 100),
		scan.Event{Done: true},
	)

	if got := model.selectedID(model.visibleRows()); got != "/root/keep" {
		t.Fatalf("selection after the target was deleted = %q, want the surviving parent", got)
	}
	if model.restorePath != "" || model.restoreID != "" || model.restoreExpanded != nil {
		t.Fatalf(
			"restore state outlived the scan: path=%q id=%q expanded=%v",
			model.restorePath,
			model.restoreID,
			model.restoreExpanded,
		)
	}
}

func TestRescanDoesNotAccumulateStaleExpansion(t *testing.T) {
	t.Parallel()

	model := testModel()
	buildTree(t, model)
	for _, id := range []string{"/root/keep", "/root/keep/deep", "/root/other"} {
		model.expanded[id] = true
	}
	selectPath(t, model, "/root/keep/deep")

	// Every rescan finds one directory fewer. Nothing may be carried forward
	// for a path the scan no longer reports.
	for round := range 3 {
		model.restart()
		events := []scan.Event{nodeEvent("/root", "", 100)}
		if round == 0 {
			events = append(events, nodeEvent("/root/keep", "/root", 50))
		}
		events = append(events, scan.Event{Done: true})
		feedEvents(t, model, events...)

		if _, stale := model.expanded["/root/keep/deep"]; stale {
			t.Fatalf("round %d resurrected expansion for a path the scan did not report", round)
		}
		if len(model.expanded) > len(model.nodes) {
			t.Fatalf(
				"round %d: expansion entries %d exceed live nodes %d",
				round,
				len(model.expanded),
				len(model.nodes),
			)
		}
		if model.restoreExpanded != nil {
			t.Fatalf("round %d left %d carried expansion paths behind", round, len(model.restoreExpanded))
		}
	}
	if got := model.selectedID(model.visibleRows()); got != "/root" {
		t.Fatalf("selection = %q once only the root survives, want /root", got)
	}
}

func TestNavigationDuringRescanCancelsRestore(t *testing.T) {
	t.Parallel()

	model := testModel()
	buildTree(t, model)
	model.expanded["/root/keep"] = true
	selectPath(t, model, "/root/keep/deep")

	model.restart()
	feedEvents(t, model,
		nodeEvent("/root", "", 300),
		nodeEvent("/root/keep", "/root", 200),
		nodeEvent("/root/other", "/root", 1),
	)
	model.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	moved := model.selectedID(model.visibleRows())

	feedEvents(t, model, nodeEvent("/root/keep/deep", "/root/keep", 100))
	if got := model.selectedID(model.visibleRows()); got != moved {
		t.Fatalf("restore moved the cursor from %q to %q after the user navigated", moved, got)
	}
}
