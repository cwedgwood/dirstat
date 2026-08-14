// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// The toolchain records the module version, the commit, its timestamp and
// whether the tree was dirty, and since Go 1.24 it derives the version from a
// VCS tag when the build sits on one. Stamping any of that with -ldflags -X
// would only duplicate it, and would go stale silently when it disagreed.
const unknownVersion = "(unknown)"

// buildDetails is what the binary can honestly say about its own provenance.
// Empty fields are unknown and are not printed: a `go install` build has no
// revision or commit time, and inventing one would mislead a bug report.
type buildDetails struct {
	version   string
	commit    string
	modified  bool
	committed string
	goVersion string
	platform  string
}

func collectBuildDetails(info *debug.BuildInfo, ok bool) buildDetails {
	details := buildDetails{
		goVersion: runtime.Version(),
		platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	if ok && info != nil {
		details.version = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				details.commit = setting.Value
			case "vcs.modified":
				details.modified = setting.Value == "true"
			case "vcs.time":
				details.committed = setting.Value
			}
		}
	}

	if details.version == "" {
		details.version = unknownVersion
	}
	return details
}

func (d buildDetails) writeTo(w io.Writer) error {
	commit := d.commit
	if commit != "" && d.modified {
		commit += " (modified)"
	}
	fields := []struct {
		label string
		value string
	}{
		{"commit", commit},
		{"committed", d.committed},
		{"go", d.goVersion},
		{"platform", d.platform},
	}

	width := 0
	for _, field := range fields {
		if field.value != "" && len(field.label) > width {
			width = len(field.label)
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "dirstat %s\n", d.version)
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		fmt.Fprintf(&out, "%-*s %s\n", width+1, field.label+":", field.value)
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func printVersion(w io.Writer) error {
	info, ok := debug.ReadBuildInfo()
	return collectBuildDetails(info, ok).writeTo(w)
}
