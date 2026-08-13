//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

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
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] [PATH...]\n\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Scan the current directory by default, or one or more explicit directories.")
		fmt.Fprintln(flag.CommandLine.Output(), "Symbolic links are inventoried but never followed.")
		fmt.Fprintln(flag.CommandLine.Output(), "\nFlags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "dirstat: --workers must be at least 1")
		return 2
	}

	roots, err := scan.ResolveRoots(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "dirstat: %v\n", err)
		return 2
	}

	model := ui.New(
		context.Background(),
		roots,
		scan.Options{
			Workers:          *workers,
			CrossFilesystems: *crossFilesystems,
		},
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
