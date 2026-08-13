// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package report

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/scan"
)

func TestCollectRanksFinalSubtreeTotals(t *testing.T) {
	t.Parallel()

	roots := []scan.Root{{Path: "/root"}}
	events := []scan.Event{
		node("/root", "", scan.StateScanning, inventory.Metrics{Files: 1}),
		node("/root/small", "/root", scan.StateComplete, inventory.Metrics{
			Files: 2, Inodes: 3, Allocated: 2048,
		}),
		node("/root/large", "/root", scan.StateScanning, inventory.Metrics{
			Files: 1, Inodes: 1, Allocated: 1,
		}),
		node("/root/large", "/root", scan.StatePartial, inventory.Metrics{
			Files: 9, Inodes: 4, Allocated: 4096, Errors: 1,
		}),
		node("/root/mount", "/root", scan.StateSkipped, inventory.Metrics{
			Inodes: 1, Allocated: 512,
		}),
		node("/elsewhere/ignored", "", scan.StateComplete, inventory.Metrics{
			Files: 1000,
		}),
		node("/root", "", scan.StateComplete, inventory.Metrics{
			Files: 11, Inodes: 9, Allocated: 8192, Errors: 1,
		}),
		{Done: true, Summary: inventory.Metrics{Files: 11, Errors: 1}},
	}

	for _, testCase := range []struct {
		name      string
		options   Options
		wantPaths []string
		wantState []scan.State
	}{
		{
			name:      "allocated ranks the whole subtree",
			options:   Options{Sort: inventory.SortAllocated},
			wantPaths: []string{"large", "small", "mount"},
			wantState: []scan.State{
				scan.StatePartial,
				scan.StateComplete,
				scan.StateSkipped,
			},
		},
		{
			name:      "top truncates after ranking",
			options:   Options{Top: 1, Sort: inventory.SortFiles},
			wantPaths: []string{"large"},
			wantState: []scan.State{scan.StatePartial},
		},
		{
			name:      "name ranks by path ascending",
			options:   Options{Sort: inventory.SortName},
			wantPaths: []string{"large", "mount", "small"},
			wantState: []scan.State{
				scan.StatePartial,
				scan.StateSkipped,
				scan.StateComplete,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			report := Collect(stream(events), roots, testCase.options)
			if report.Cancelled {
				t.Fatal("completed stream reported as cancelled")
			}
			if report.Errors != 1 {
				t.Fatalf("errors = %d, want 1", report.Errors)
			}
			if len(report.Roots) != 1 {
				t.Fatalf("roots = %d, want 1", len(report.Roots))
			}
			root := report.Roots[0]
			if root.Totals.Files != 11 || root.Totals.Inodes != 9 {
				t.Fatalf("root totals = %+v", root.Totals)
			}

			paths := make([]string, 0, len(root.Entries))
			states := make([]scan.State, 0, len(root.Entries))
			for _, entry := range root.Entries {
				paths = append(paths, entry.Path)
				states = append(states, entry.State)
			}
			if strings.Join(paths, ",") != strings.Join(testCase.wantPaths, ",") {
				t.Fatalf("paths = %v, want %v", paths, testCase.wantPaths)
			}
			if fmt.Sprint(states) != fmt.Sprint(testCase.wantState) {
				t.Fatalf("states = %v, want %v", states, testCase.wantState)
			}
		})
	}
}

func TestCollectReportsAbandonedScan(t *testing.T) {
	t.Parallel()

	roots := []scan.Root{{Path: "/root"}}
	for _, testCase := range []struct {
		name   string
		events []scan.Event
		want   bool
	}{
		{name: "closed without a terminal event", events: nil, want: true},
		{
			name:   "cancelled",
			events: []scan.Event{{Done: true, Cancelled: true}},
			want:   true,
		},
		{name: "done", events: []scan.Event{{Done: true}}, want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			report := Collect(stream(testCase.events), roots, Options{})
			if report.Cancelled != testCase.want {
				t.Fatalf("cancelled = %t, want %t", report.Cancelled, testCase.want)
			}
		})
	}
}

func TestWriteRendersRankedTable(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		report  Report
		options Options
		want    string
	}{
		{
			name: "human readable units",
			report: Report{Roots: []RootReport{{
				Path: "/home/user",
				Totals: inventory.Metrics{
					Directories: 1200, Files: 2500, Inodes: 3700,
					Allocated: 1536 * 1024 * 1024, Apparent: 1024 * 1024 * 1024,
				},
				Entries: []Entry{
					{
						Path:  ".cache",
						State: scan.StateComplete,
						Metrics: inventory.Metrics{
							Files: 1500, Inodes: 1600, Allocated: 1024 * 1024 * 1024,
							Apparent: 512 * 1024 * 1024,
						},
					},
					{
						Path:  "media/mnt",
						State: scan.StateSkipped,
						Metrics: inventory.Metrics{
							Inodes: 1, Allocated: 4096, Apparent: 4096,
						},
					},
				},
			}}},
			want: "root /home/user  dirs 1.2K  files 2.5K  inodes 3.7K" +
				"  alloc 1.5GiB  apparent 1.0GiB  errors 0\n" +
				"    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n" +
				"     1.5K      1.6K     1.0GiB   512.0MiB  ok      .cache\n" +
				"        0         1     4.0KiB     4.0KiB  mount   media/mnt\n",
		},
		{
			name: "exact numbers",
			report: Report{Roots: []RootReport{{
				Path:   "/srv",
				Totals: inventory.Metrics{Directories: 2, Files: 3, Inodes: 5, Allocated: 9216},
				Entries: []Entry{{
					Path:    "data",
					State:   scan.StatePartial,
					Metrics: inventory.Metrics{Files: 3, Inodes: 4, Allocated: 5120, Errors: 2},
				}},
			}}},
			options: Options{Exact: true},
			want: "root /srv  dirs 2  files 3  inodes 5  alloc 9216  apparent 0  errors 0\n" +
				"    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n" +
				"        3         4       5120          0  partial data\n",
		},
		{
			name: "hostile names cannot forge output",
			report: Report{Roots: []RootReport{{
				Path: "/root",
				Entries: []Entry{{
					Path:  "evil\n\x1b[2Jrow",
					State: scan.StateComplete,
				}},
			}}},
			want: "root /root  dirs 0  files 0  inodes 0  alloc 0B  apparent 0B  errors 0\n" +
				"    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n" +
				`        0         0         0B         0B  ok      evil\n\x1b[2Jrow` + "\n",
		},
		{
			name: "multiple roots are separate sections",
			report: Report{Roots: []RootReport{
				{Path: "/first", Totals: inventory.Metrics{Directories: 1}},
				{
					Path:    "/second",
					Totals:  inventory.Metrics{Directories: 2, Files: 1},
					Entries: []Entry{{Path: "child", State: scan.StateComplete}},
				},
			}},
			want: "root /first  dirs 1  files 0  inodes 0  alloc 0B  apparent 0B  errors 0\n" +
				"    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n" +
				"\n" +
				"root /second  dirs 2  files 1  inodes 0  alloc 0B  apparent 0B  errors 0\n" +
				"    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n" +
				"        0         0         0B         0B  ok      child\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			builder := &strings.Builder{}
			if err := Write(builder, testCase.report, testCase.options); err != nil {
				t.Fatal(err)
			}
			if got := builder.String(); got != testCase.want {
				t.Fatalf("Write output:\n%s\nwant:\n%s", got, testCase.want)
			}
		})
	}
}

func TestWriteDiagnostics(t *testing.T) {
	t.Parallel()

	many := make([]string, 0, maxDiagnostics+2)
	for index := range maxDiagnostics + 2 {
		many = append(many, fmt.Sprintf("sample %d", index))
	}

	for _, testCase := range []struct {
		name   string
		report Report
		want   string
	}{
		{name: "silent without errors", report: Report{Errors: 0}},
		{
			name: "escapes samples",
			report: Report{
				Errors: 2,
				Roots: []RootReport{{
					Samples: []string{"/root/a\x1b[2J: open directory: denied"},
				}},
			},
			want: "dirstat: 2 errors during scan; totals are partial\n" +
				`dirstat: /root/a\x1b[2J: open directory: denied` + "\n",
		},
		{
			name:   "caps samples",
			report: Report{Errors: 99, Roots: []RootReport{{Samples: many}}},
			want: "dirstat: 99 errors during scan; totals are partial\n" +
				"dirstat: sample 0\ndirstat: sample 1\ndirstat: sample 2\n" +
				"dirstat: sample 3\ndirstat: sample 4\ndirstat: sample 5\n" +
				"dirstat: sample 6\ndirstat: sample 7\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			builder := &strings.Builder{}
			writeDiagnostics(builder, testCase.report)
			if got := builder.String(); got != testCase.want {
				t.Fatalf("diagnostics = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRunOverTemporaryTree(t *testing.T) {
	t.Parallel()

	root := buildTree(t)
	header := "    FILES    INODES      ALLOC   APPARENT  STATE   PATH\n"

	for _, testCase := range []struct {
		name    string
		options Options
		want    string
	}{
		{
			name:    "top three by files",
			options: Options{Top: 3, Sort: inventory.SortFiles},
			want: "root %s  dirs 6  files 11  inodes 17  alloc -  apparent -  errors 0\n" +
				header +
				"        8        10          -          -  ok      bulk\n" +
				"        3         4          -          -  ok      bulk/deep\n" +
				"        2         3          -          -  ok      keep\n",
		},
		{
			name:    "every directory by inodes",
			options: Options{Sort: inventory.SortInodes},
			want: "root %s  dirs 6  files 11  inodes 17  alloc -  apparent -  errors 0\n" +
				header +
				"        8        10          -          -  ok      bulk\n" +
				"        3         4          -          -  ok      bulk/deep\n" +
				"        2         3          -          -  ok      keep\n" +
				`        1         2          -          -  ok      odd\n\x1bname` + "\n" +
				"        0         1          -          -  ok      empty\n",
		},
		{
			name:    "name orders by path",
			options: Options{Sort: inventory.SortName, Exact: true},
			want: "root %s  dirs 6  files 11  inodes 17  alloc -  apparent -  errors 0\n" +
				header +
				"        8        10          -          -  ok      bulk\n" +
				"        3         4          -          -  ok      bulk/deep\n" +
				"        0         1          -          -  ok      empty\n" +
				"        2         3          -          -  ok      keep\n" +
				`        1         2          -          -  ok      odd\n\x1bname` + "\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			roots, err := scan.ResolveRoots([]string{root})
			if err != nil {
				t.Fatal(err)
			}
			output := &strings.Builder{}
			diagnostics := &strings.Builder{}
			if err := Run(
				context.Background(),
				roots,
				scan.Options{Workers: 2},
				testCase.options,
				output,
				diagnostics,
			); err != nil {
				t.Fatal(err)
			}
			if diagnostics.String() != "" {
				t.Fatalf("unexpected diagnostics: %q", diagnostics.String())
			}

			want := fmt.Sprintf(testCase.want, roots[0].Path)
			if got := hideByteColumns(output.String()); got != want {
				t.Fatalf("Run output:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// buildTree creates a fixed directory tree. The awkward name proves that a
// hostile directory cannot inject output through a real scan, not only through
// a synthesized report.
func buildTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	for directory, files := range map[string]int{
		"keep":          2,
		"bulk":          5,
		"bulk/deep":     3,
		"empty":         0,
		"odd\n\x1bname": 1,
	} {
		path := filepath.Join(root, directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := range files {
			name := filepath.Join(path, fmt.Sprintf("file%02d", index))
			if err := os.WriteFile(name, []byte("content"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// hideByteColumns replaces the allocated and apparent columns. Both depend on
// the filesystem holding the temporary tree, so only the columns the test can
// predict are compared.
func hideByteColumns(output string) string {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "root "):
			fields := strings.Split(line, "  ")
			for fieldIndex, field := range fields {
				if strings.HasPrefix(field, "alloc ") ||
					strings.HasPrefix(field, "apparent ") {
					fields[fieldIndex] = strings.SplitN(field, " ", 2)[0] + " -"
				}
			}
			lines[index] = strings.Join(fields, "  ")
		case strings.Contains(line, "APPARENT"):
		case len(line) > 41:
			lines[index] = line[:20] + fmt.Sprintf("%10s %10s", "-", "-") + line[41:]
		}
	}
	return strings.Join(lines, "\n")
}

func node(
	path string,
	parent string,
	state scan.State,
	metrics inventory.Metrics,
) scan.Event {
	return scan.Event{Node: &scan.NodeUpdate{
		ID:       path,
		ParentID: parent,
		Path:     path,
		Name:     filepath.Base(path),
		Root:     parent == "",
		State:    state,
		Metrics:  metrics,
	}}
}

func stream(events []scan.Event) <-chan scan.Event {
	channel := make(chan scan.Event, len(events)+1)
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return channel
}
