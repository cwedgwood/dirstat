// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

// Package report scans directory trees to completion and produces a flat
// ranked list of the directories that hold the most of one metric.
//
// It consumes the scanner event stream directly and never touches the
// terminal, so it works unchanged when stdout is a file or a pipe. The
// collected [Report] is a plain data structure holding raw paths and exact
// numbers; rendering decisions such as unit scaling and escaping belong to the
// writer, which is what keeps other output formats a matter of adding a writer.
package report

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cwedgwood/dirstat/internal/format"
	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/scan"
)

// maxDiagnostics bounds the error samples written to the diagnostics stream so
// a tree that fails everywhere cannot bury the ranking in complaints.
const maxDiagnostics = 8

// Options controls ranking and rendering.
type Options struct {
	// Top limits how many directories are listed per scan root. Zero lists
	// every directory.
	Top int

	// Sort selects the metric that ranks directories.
	Sort inventory.SortField

	// Exact prints unscaled numbers instead of human-readable units.
	Exact bool
}

// Entry is one ranked directory. Path is relative to the scan root and is not
// escaped; a writer escapes it for the destination it targets.
type Entry struct {
	Path    string
	State   scan.State
	Metrics inventory.Metrics
}

// RootReport is the ranking for one scan root.
type RootReport struct {
	// Path is the absolute scan root.
	Path string

	// Totals is the whole-tree roll-up for the root itself.
	Totals inventory.Metrics

	// Entries are the ranked directories below the root, already truncated
	// to Options.Top.
	Entries []Entry

	// Samples are error samples the scanner rolled up to the root.
	Samples []string
}

// Report is the outcome of one non-interactive scan.
type Report struct {
	Roots     []RootReport
	Errors    uint64
	Cancelled bool
}

// ErrCancelled reports a scan that ended before every root was complete.
var ErrCancelled = errors.New("scan cancelled before completion")

// Run scans the roots to completion and writes a ranked report to output,
// sending scan errors that only made the result partial to diagnostics.
func Run(
	ctx context.Context,
	roots []scan.Root,
	scanOptions scan.Options,
	options Options,
	output io.Writer,
	diagnostics io.Writer,
) error {
	scanContext, cancel := context.WithCancel(ctx)
	defer cancel()

	// Collect ranks final roll-ups and drops everything else, so asking the
	// scanner for intermediate states would only pay to build and deliver
	// updates this function throws away.
	scanOptions.FinalStatesOnly = true

	report := Collect(scan.Start(scanContext, roots, scanOptions), roots, options)
	if report.Cancelled {
		// Ranking a tree that was never finished would silently understate
		// every subtree that was still being counted.
		return ErrCancelled
	}
	if err := Write(output, report, options); err != nil {
		return err
	}
	writeDiagnostics(diagnostics, report)
	return nil
}

// Collect drains a scanner event stream into a ranked report.
func Collect(events <-chan scan.Event, roots []scan.Root, options Options) Report {
	report := Report{
		Roots: make([]RootReport, len(roots)),
		// A stream that ends without a terminal event was abandoned.
		Cancelled: true,
	}
	entries := make([][]Entry, len(roots))
	for index, root := range roots {
		report.Roots[index].Path = root.Path
	}

	for event := range events {
		if event.Done {
			report.Cancelled = event.Cancelled
			report.Errors = event.Summary.Errors
		}
		node := event.Node
		if node == nil || !final(node.State) {
			continue
		}
		index, relative, ok := locate(roots, node.Path)
		if !ok {
			continue
		}
		if relative == "." {
			report.Roots[index].Totals = node.Metrics
			report.Roots[index].Samples = node.ErrorSamples
			continue
		}
		entries[index] = append(entries[index], Entry{
			Path:    relative,
			State:   node.State,
			Metrics: node.Metrics,
		})
	}

	for index := range report.Roots {
		rank(entries[index], options.Sort)
		if options.Top > 0 && len(entries[index]) > options.Top {
			entries[index] = entries[index][:options.Top]
		}
		report.Roots[index].Entries = entries[index]
	}
	return report
}

// rank orders directories by the chosen metric, largest first, breaking ties by
// path so that identical trees always produce identical output.
func rank(entries []Entry, field inventory.SortField) {
	sort.Slice(entries, func(leftIndex int, rightIndex int) bool {
		left := entries[leftIndex]
		right := entries[rightIndex]
		if comparison := field.Compare(left.Metrics, right.Metrics); comparison != 0 {
			return comparison > 0
		}
		return left.Path < right.Path
	})
}

// final reports whether a node state is the last one the scanner will emit for
// that directory. Ranking anything else would use a roll-up still in flight.
func final(state scan.State) bool {
	switch state {
	case scan.StateComplete, scan.StatePartial, scan.StateSkipped, scan.StateAlias:
		return true
	default:
		return false
	}
}

// locate finds the root a directory belongs to and its path below that root.
// Scan roots are validated as non-overlapping, so at most one can match.
func locate(roots []scan.Root, path string) (int, string, bool) {
	for index, root := range roots {
		relative, err := filepath.Rel(root.Path, path)
		if err != nil {
			continue
		}
		if relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return index, relative, true
	}
	return 0, "", false
}

func writeDiagnostics(diagnostics io.Writer, report Report) {
	if diagnostics == nil || report.Errors == 0 {
		return
	}
	fmt.Fprintf(
		diagnostics,
		"dirstat: %d errors during scan; totals are partial\n",
		report.Errors,
	)
	written := 0
	for _, root := range report.Roots {
		for _, sample := range root.Samples {
			if written >= maxDiagnostics {
				return
			}
			fmt.Fprintf(diagnostics, "dirstat: %s\n", format.Display(sample))
			written++
		}
	}
}
