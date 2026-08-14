// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/cwedgwood/dirstat/internal/format"
	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/scan"
)

// widthFixture is a directory name whose displayed width in terminal cells is
// not its rune count. cells is the width of format.Display(name), computed by
// hand, so a change in the escaping or measurement rules is visible here rather
// than silently agreeing with whatever the code does.
type widthFixture struct {
	label string
	name  string
	cells int
}

var widthFixtures = []widthFixture{
	{label: "cjk", name: "写真", cells: 4},
	{label: "cjk-long", name: "日本語のディレクトリ", cells: 20},
	// The ZWJ joiners are format characters, so displayText escapes them to
	// \u200d and the escaped form is what has to be measured: four wide emoji
	// plus three six-character escapes plus the ASCII prefix.
	{
		label: "emoji-zwj",
		name:  "family\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466",
		cells: 32,
	},
	// U+FE0F survives escaping and turns the preceding narrow symbol into a
	// two-cell emoji presentation cluster.
	{label: "emoji-variation", name: "smile\u263a\ufe0f", cells: 7},
	{label: "combining", name: "cafe\u0301-photos", cells: 11},
	// East Asian Ambiguous: rendered narrow, which is what a Linux terminal
	// does unless it is explicitly configured otherwise.
	{label: "ambiguous", name: "\u03b1\u03b2-data", cells: 7},
	{label: "rtl", name: "\u0645\u062c\u0644\u062f", cells: 4},
	{label: "mixed", name: "video-動画-01", cells: 13},
	{label: "long", name: strings.Repeat("長い名前", 12), cells: 96},
	{label: "ascii", name: "documents", cells: 9},
}

func TestFixtureDisplayWidths(t *testing.T) {
	t.Parallel()

	for _, fixture := range widthFixtures {
		text := format.Display(fixture.name)
		if got := ansi.StringWidth(text); got != fixture.cells {
			t.Errorf("%s: display width of %q = %d, want %d", fixture.label, text, got, fixture.cells)
		}
	}
}

func TestRenderRowKeepsColumnsAlignedInCells(t *testing.T) {
	t.Parallel()

	for _, width := range []int{70, 71, 80, 100} {
		model := testModel()
		model.width = width
		model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
			ID:      "/root",
			Path:    "/root",
			Name:    "root",
			Root:    true,
			State:   scan.StateScanning,
			Metrics: inventory.Metrics{Files: 42},
		}})
		baseline := ansi.StringWidth(model.renderRow(row{id: "/root"}, width))
		nameWidth := max(8, width-55)

		if got := ansi.StringWidth(model.renderColumns(width)); got != baseline {
			t.Errorf("width %d: header width = %d cells, want %d", width, got, baseline)
		}

		for _, fixture := range widthFixtures {
			id := "/root/" + fixture.label
			model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
				ID:       id,
				ParentID: "/root",
				Path:     id,
				Name:     fixture.name,
				State:    scan.StateScanning,
				Metrics:  inventory.Metrics{Files: 42},
			}})

			line := model.renderRow(row{id: id, depth: 1}, width)
			if got := ansi.StringWidth(line); got != baseline {
				t.Errorf(
					"width %d: %s row = %d cells, want %d: %q",
					width,
					fixture.label,
					got,
					baseline,
					line,
				)
			}
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: %s row overflows terminal by %d cells", width, fixture.label, got-width)
			}
			index := strings.Index(line, "updating...")
			if index < 0 {
				t.Fatalf("width %d: %s row lost its status column: %q", width, fixture.label, line)
			}
			if got := ansi.StringWidth(line[:index]); got != nameWidth+1 {
				t.Errorf(
					"width %d: %s status column starts at cell %d, want %d: %q",
					width,
					fixture.label,
					got,
					nameWidth+1,
					line,
				)
			}
		}
	}
}

func TestTruncateMeasuresCells(t *testing.T) {
	t.Parallel()

	for _, fixture := range widthFixtures {
		text := format.Display(fixture.name)
		for limit := 1; limit <= fixture.cells+2; limit++ {
			got := truncate(text, limit)
			if width := ansi.StringWidth(got); width > limit {
				t.Errorf(
					"%s: truncate(%q, %d) = %q is %d cells wide",
					fixture.label,
					text,
					limit,
					got,
					width,
				)
			}
			assertNoSplitCluster(t, fixture.label, text, got)
		}
	}
}

func TestTruncateKeepsGraphemeClustersWhole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label string
		value string
		limit int
	}{
		{label: "family-emoji", value: "\U0001F468\u200d\U0001F469\u200d\U0001F467abc", limit: 3},
		{label: "variation-selector", value: "\u263a\ufe0fxyz", limit: 2},
		{label: "combining", value: "e\u0301e\u0301e\u0301", limit: 2},
		{label: "wide-boundary", value: "ab日本語", limit: 5},
	}
	for _, testCase := range cases {
		got := truncate(testCase.value, testCase.limit)
		if width := ansi.StringWidth(got); width > testCase.limit {
			t.Errorf("%s: truncate(%q, %d) = %q is %d cells", testCase.label, testCase.value, testCase.limit, got, width)
		}
		assertNoSplitCluster(t, testCase.label, testCase.value, got)
	}
}

func TestChromeLinesFitTerminalWidth(t *testing.T) {
	t.Parallel()

	const width = 40
	wide := strings.Repeat("日本語", 20)

	model := testModel()
	model.width = width
	model.height = 24
	model.details = true
	model.filter = wide
	model.progress = scan.Progress{Scanned: 1, Discovered: 2}
	model.applyEvent(scan.Event{Node: &scan.NodeUpdate{
		ID:           "/" + wide,
		Path:         "/" + wide,
		Name:         wide,
		Root:         true,
		State:        scan.StatePartial,
		ErrorSamples: []string{wide + ": permission denied"},
	}})

	lines := []string{
		model.renderStatus(width),
		model.renderSortAndFilter(width),
		model.renderSortPicker(width),
	}
	lines = append(lines, model.renderDetails(width, model.visibleRows())...)
	for index, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("line %d is %d cells wide, want at most %d: %q", index, got, width, line)
		}
	}
}

func TestStatusLinePlacesElapsedHardRight(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.options.NoColor = true
	model.progress = scan.Progress{Scanned: 1, Discovered: 2, Elapsed: 4190 * time.Millisecond}

	for _, width := range []int{40, 80, 120} {
		line := model.renderStatus(width)
		if got := ansi.StringWidth(line); got != width {
			t.Errorf("width %d: status line is %d cells wide", width, got)
		}
		if !strings.HasSuffix(line, "elapsed 4.19s") {
			t.Errorf("width %d: elapsed time is not hard right: %q", width, line)
		}
		if !strings.HasPrefix(line, "dirstat |") {
			t.Errorf("width %d: counters were dropped with room to spare: %q", width, line)
		}
	}
}

func TestNarrowStatusLineKeepsTheClockAndDropsTheCounters(t *testing.T) {
	t.Parallel()

	model := testModel()
	model.options.NoColor = true
	model.progress = scan.Progress{Scanned: 1, Discovered: 2, Elapsed: 4190 * time.Millisecond}

	for _, width := range []int{1, 5, 13, 14} {
		line := model.renderStatus(width)
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("width %d: status line is %d cells wide", width, got)
		}
		if strings.Contains(line, "dirstat") {
			t.Errorf("width %d: counters kept in place of the clock: %q", width, line)
		}
	}
	if got := model.renderStatus(13); got != "elapsed 4.19s" {
		t.Errorf("exact-fit status line = %q, want the whole clock", got)
	}
}

// assertNoSplitCluster checks that a truncation result is a grapheme-cluster
// boundary prefix of its input: a kept prefix that ends immediately before a
// combining mark would render the mark on whatever follows it in the row.
func assertNoSplitCluster(t *testing.T, label string, value string, got string) {
	t.Helper()

	kept := strings.TrimSuffix(got, "...")
	if !strings.HasPrefix(value, kept) {
		return
	}
	rest := value[len(kept):]
	if rest == "" {
		return
	}
	next := []rune(rest)[0]
	if unicode.In(next, unicode.Mn, unicode.Me, unicode.Mc) {
		t.Errorf("%s: truncate(%q) = %q strands combining mark %U", label, value, got, next)
	}
}
