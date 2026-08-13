// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package report

import (
	"bufio"
	"fmt"
	"io"
	"strconv"

	"github.com/cwedgwood/dirstat/internal/format"
	"github.com/cwedgwood/dirstat/internal/scan"
)

// rowFormat puts every fixed-width column before the path, because a path is
// the only field whose width and content are unbounded.
const rowFormat = "%9s %9s %10s %10s  %-7s %s\n"

// Write renders a report as plain text. No styling is emitted: the output is
// the same whether it lands on a terminal, a file, or a pipe.
func Write(destination io.Writer, report Report, options Options) error {
	writer := bufio.NewWriter(destination)
	for index, root := range report.Roots {
		if index > 0 {
			fmt.Fprintln(writer)
		}
		writeRoot(writer, root, options)
	}
	// bufio.Writer holds the first write error and returns it from Flush.
	return writer.Flush()
}

func writeRoot(writer io.Writer, root RootReport, options Options) {
	fmt.Fprintf(
		writer,
		"root %s  dirs %s  files %s  inodes %s  alloc %s  apparent %s  errors %s\n",
		format.Display(root.Path),
		options.count(root.Totals.Directories),
		options.count(root.Totals.Files),
		options.count(root.Totals.Inodes),
		options.bytes(root.Totals.Allocated),
		options.bytes(root.Totals.Apparent),
		options.count(root.Totals.Errors),
	)
	fmt.Fprintf(writer, rowFormat, "FILES", "INODES", "ALLOC", "APPARENT", "STATE", "PATH")
	for _, entry := range root.Entries {
		fmt.Fprintf(
			writer,
			rowFormat,
			options.count(entry.Metrics.Files),
			options.count(entry.Metrics.Inodes),
			options.bytes(entry.Metrics.Allocated),
			options.bytes(entry.Metrics.Apparent),
			stateName(entry.State),
			format.Display(entry.Path),
		)
	}
}

func (o Options) count(value uint64) string {
	if o.Exact {
		return strconv.FormatUint(value, 10)
	}
	return format.Count(value)
}

func (o Options) bytes(value uint64) string {
	if o.Exact {
		return strconv.FormatUint(value, 10)
	}
	return format.Bytes(value)
}

func stateName(state scan.State) string {
	switch state {
	case scan.StateComplete:
		return "ok"
	case scan.StatePartial:
		return "partial"
	case scan.StateSkipped:
		return "mount"
	case scan.StateAlias:
		return "alias"
	default:
		return string(state)
	}
}
