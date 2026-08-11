package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dirstat/internal/scan"
)

func TestProgramStartsScansAndQuits(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, err := scan.ResolveRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}

	model := New(
		context.Background(),
		roots,
		scan.Options{Workers: 1},
		Options{NoColor: true},
	)
	defer model.Close()

	program := tea.NewProgram(
		model,
		tea.WithInput(strings.NewReader("q")),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
	)
	finished := make(chan error, 1)
	go func() {
		_, runErr := program.Run()
		finished <- runErr
	}()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		program.Kill()
		t.Fatal("Bubble Tea program did not respond to quit input")
	}
}
