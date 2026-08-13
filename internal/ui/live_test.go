package ui

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cwedgwood/dirstat/internal/scan"
)

func TestLiveUI(t *testing.T) {
	path := os.Getenv("INODE_COUNTER_UI_LIVE_ROOT")
	if path == "" {
		t.Skip("set INODE_COUNTER_UI_LIVE_ROOT to run a live UI-model scan")
	}

	roots, err := scan.ResolveRoots([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	model := New(
		ctx,
		roots,
		scan.Options{Workers: scan.DefaultWorkers()},
		Options{NoColor: true},
	)
	defer model.Close()

	start := time.Now()
	command := model.Init()
	batches := 0
	for !model.done {
		if command == nil {
			t.Fatal("UI event loop stopped before scan completion")
		}
		message := command()
		_, command = model.Update(message)
		_ = model.View()
		batches++
	}
	if model.cancelled {
		t.Fatalf("live UI scan of %s did not finish within one minute", path)
	}
	t.Logf(
		"live UI scan %s: %d directories, %d batches, %s",
		path,
		model.progress.Scanned,
		batches,
		time.Since(start),
	)
}
