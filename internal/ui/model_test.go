// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
