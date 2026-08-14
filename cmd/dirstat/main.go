// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/report"
	"github.com/cwedgwood/dirstat/internal/scan"
	"github.com/cwedgwood/dirstat/internal/ui"
)

// options are the parsed command line, gathered so the flag surface can be
// declared once and inspected by tests without a real command line.
type options struct {
	workers          int
	crossFilesystems bool
	noColor          bool
	top              int
	sortName         string
	exact            bool
	version          bool
}

func newFlagSet(name string, out io.Writer, opts *options) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ExitOnError)
	flags.SetOutput(out)
	flags.IntVar(
		&opts.workers,
		"workers",
		scan.DefaultWorkers(),
		"maximum number of directories scanned concurrently",
	)
	flags.BoolVar(
		&opts.crossFilesystems,
		"cross-filesystems",
		false,
		"descend into mounted filesystems",
	)
	flags.BoolVar(
		&opts.noColor,
		"no-color",
		false,
		"disable color and terminal styling in the interactive tree; ranked --top output is never styled",
	)
	flags.IntVar(
		&opts.top,
		"top",
		0,
		"print the N largest directories and exit instead of starting the interactive tree; --top 0 prints every directory",
	)
	flags.StringVar(
		&opts.sortName,
		"sort",
		inventory.SortAllocated.String(),
		"metric to sort by: allocated, inodes, files, apparent, or name; ranks --top output and sets the interactive tree's initial order",
	)
	flags.BoolVar(
		&opts.exact,
		"exact",
		false,
		"print unscaled numbers in --top output instead of scaled units",
	)
	flags.BoolVar(&opts.version, "version", false, "print version information and exit")
	flags.Usage = func() { usage(flags) }
	return flags
}

func usage(flags *flag.FlagSet) {
	out := flags.Output()
	fmt.Fprintf(out, "Usage: %s [flags] [PATH...]\n\n", flags.Name())
	fmt.Fprint(out, `Scan the current directory by default, or one or more explicit directories.
Roots must be directories and may not overlap. Symbolic links are inventoried
but never followed, and traversal stays on the filesystem holding each root
unless --cross-filesystems is given.

Without --top an interactive tree is shown. With --top a ranked list of the
directories holding the most of one metric is written to standard output and
dirstat exits; that list is plain text, so it can be redirected or piped.
--sort ranks that list and sets the order the tree opens in; --exact applies to
--top output only.

The exit status is 0 when the scan completed, 1 when it did not, and 2 for a
usage error.

Flags:
`)
	flags.PrintDefaults()
}

func main() {
	os.Exit(run())
}

func run() int {
	var opts options
	flags := newFlagSet(os.Args[0], os.Stderr, &opts)
	// ExitOnError has already reported and exited on a malformed command line.
	_ = flags.Parse(os.Args[1:])

	// Answered before validation, root resolution, and any terminal setup, so
	// it works when stdout is a pipe or a file.
	if opts.version {
		if err := printVersion(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
			return 1
		}
		return 0
	}

	oneShot := false
	exactSet := false
	flags.Visit(func(set *flag.Flag) {
		switch set.Name {
		case "top":
			oneShot = true
		case "exact":
			exactSet = true
		}
	})

	if opts.workers < 1 {
		fmt.Fprintln(os.Stderr, "dirstat: --workers must be at least 1")
		return 2
	}

	if opts.top < 0 {
		fmt.Fprintln(os.Stderr, "dirstat: --top may not be negative")
		return 2
	}
	sortField, err := inventory.ParseSortField(opts.sortName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 2
	}
	// --sort is accepted in both modes: which metric matters is as much a
	// question when browsing a tree as when ranking one, and the tree already
	// offers the same five choices through its own menu. --exact stays
	// --top-only, because it is about printing unscaled numbers and the tree
	// never does that.
	if !oneShot && exactSet {
		fmt.Fprintln(os.Stderr, "dirstat: --exact only applies to --top output")
		return 2
	}

	roots, err := scan.ResolveRoots(flags.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 2
	}

	scanOptions := scan.Options{
		Workers:          opts.workers,
		CrossFilesystems: opts.crossFilesystems,
	}

	if oneShot {
		err := report.Run(
			context.Background(),
			roots,
			scanOptions,
			report.Options{Top: opts.top, Sort: sortField, Exact: opts.exact},
			os.Stdout,
			os.Stderr,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
			return 1
		}
		return 0
	}

	model := ui.New(
		context.Background(),
		roots,
		scanOptions,
		ui.Options{NoColor: opts.noColor, Sort: sortField},
	)
	defer model.Close()

	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 1
	}
	return 0
}
