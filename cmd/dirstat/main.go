// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/report"
	"github.com/cwedgwood/dirstat/internal/scan"
	"github.com/cwedgwood/dirstat/internal/ui"
)

func main() {
	os.Exit(run())
}

func run() int {
	workers := flag.Int(
		"workers",
		scan.DefaultWorkers(),
		"maximum number of directories scanned concurrently",
	)
	crossFilesystems := flag.Bool(
		"cross-filesystems",
		false,
		"descend into mounted filesystems",
	)
	noColor := flag.Bool("no-color", false, "disable color and terminal styling")
	top := flag.Int(
		"top",
		0,
		"print the N largest directories and exit instead of starting the interactive tree; 0 prints every directory",
	)
	sortName := flag.String(
		"sort",
		inventory.SortAllocated.String(),
		"metric that ranks --top output: allocated, inodes, files, apparent, or name",
	)
	exact := flag.Bool("exact", false, "print unscaled numbers in --top output instead of units")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] [PATH...]\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Scan the current directory by default, or one or more explicit directories.")
		fmt.Fprintln(flag.CommandLine.Output(), "Symbolic links are inventoried but never followed.")
		fmt.Fprintln(flag.CommandLine.Output(), "Without --top an interactive tree is shown; with it a ranked list is printed and dirstat exits.")
		fmt.Fprintln(flag.CommandLine.Output(), "\nFlags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	oneShot := false
	sortSet := false
	exactSet := false
	flag.Visit(func(set *flag.Flag) {
		switch set.Name {
		case "top":
			oneShot = true
		case "sort":
			sortSet = true
		case "exact":
			exactSet = true
		}
	})

	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "dirstat: --workers must be at least 1")
		return 2
	}

	if *top < 0 {
		fmt.Fprintln(os.Stderr, "dirstat: --top may not be negative")
		return 2
	}
	sortField, err := inventory.ParseSortField(*sortName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 2
	}
	if !oneShot && (sortSet || exactSet) {
		fmt.Fprintln(os.Stderr, "dirstat: --sort and --exact only apply to --top output")
		return 2
	}

	roots, err := scan.ResolveRoots(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 2
	}

	scanOptions := scan.Options{
		Workers:          *workers,
		CrossFilesystems: *crossFilesystems,
	}

	if oneShot {
		err := report.Run(
			context.Background(),
			roots,
			scanOptions,
			report.Options{Top: *top, Sort: sortField, Exact: *exact},
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
		ui.Options{NoColor: *noColor},
	)
	defer model.Close()

	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 1
	}
	return 0
}
