// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwedgwood/dirstat/internal/scan"
)

// TestScannedTreeLinksEveryDirectoryExactlyOnce guards the property the
// scanner's emission order exists to protect: a directory that reaches the
// model before its parent is counted in every ancestor's totals but is never
// attached to the tree, so it silently disappears from the display.
func TestScannedTreeLinksEveryDirectoryExactlyOnce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for branch := range 6 {
		path := root
		for depth := range 8 {
			path = filepath.Join(path, string(rune('a'+branch))+string(rune('0'+depth)))
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "file"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	roots, err := scan.ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	model := New(
		context.Background(),
		roots,
		scan.Options{Workers: 4},
		Options{NoColor: true},
	)
	defer model.Close()

	for command := model.Init(); command != nil; {
		_, command = model.Update(command())
	}
	if model.cancelled {
		t.Fatal("scan was cancelled")
	}

	visited := make(map[string]int)
	var walk func(*treeNode)
	walk = func(node *treeNode) {
		visited[node.id]++
		for _, child := range node.children {
			walk(child)
		}
	}
	for _, node := range model.rootNodes() {
		walk(node)
	}

	if len(model.orphans) != 0 {
		t.Fatalf("%d directories arrived before their parent", len(model.orphans))
	}
	if uint64(len(model.nodes)) != model.summary.Directories {
		t.Fatalf(
			"model holds %d directories, scan counted %d",
			len(model.nodes),
			model.summary.Directories,
		)
	}
	if len(visited) != len(model.nodes) {
		t.Fatalf(
			"%d of %d directories are unreachable from a root",
			len(model.nodes)-len(visited),
			len(model.nodes),
		)
	}
	for id, count := range visited {
		if count != 1 {
			t.Fatalf("%s appears %d times in the tree, want once", id, count)
		}
	}
}

// TestLateParentAdoptsChildrenAlreadySeen covers the model's own defence:
// linking is attempted only when a directory is first met, so a child that
// arrives early has to be adopted when its parent shows up.
func TestLateParentAdoptsChildrenAlreadySeen(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:       "/root/child",
		ParentID: "/root",
		Path:     "/root/child",
		Name:     "child",
		State:    scan.StateComplete,
	}})
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:    "/root",
		Path:  "/root",
		Name:  "root",
		Root:  true,
		State: scan.StateComplete,
	}})

	parent := model.nodes["/root"]
	if parent == nil || len(parent.children) != 1 ||
		parent.children[0] != model.nodes["/root/child"] {
		t.Fatalf("late parent did not adopt the child it already had: %+v", parent)
	}
	if len(model.orphans) != 0 {
		t.Fatalf("adopted child was left waiting: %+v", model.orphans)
	}
	rows := model.visibleRows()
	if len(rows) != 2 {
		t.Fatalf("tree shows %d rows, want the root and its child", len(rows))
	}
}
