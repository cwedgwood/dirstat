// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Path: "github.com/cwedgwood/dirstat", Version: mainVersion},
		Settings: settings,
	}
}

func TestCollectBuildDetailsReadsTheTagFromBuildInfo(t *testing.T) {
	t.Parallel()

	// Since Go 1.24 the toolchain derives the module version from a VCS tag
	// when the build sits on one, which is where a release gets its name.
	details := collectBuildDetails(buildInfo("v1.2.3",
		debug.BuildSetting{Key: "vcs.revision", Value: "6b4d1c0"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-08-13T07:19:26Z"},
	), true)

	if details.version != "v1.2.3" {
		t.Fatalf("version = %q, want the tag the toolchain recorded", details.version)
	}
	if details.commit != "6b4d1c0" {
		t.Fatalf("commit = %q, want the recorded revision", details.commit)
	}
	if details.modified {
		t.Fatal("clean tree reported as modified")
	}
	if details.committed != "2026-08-13T07:19:26Z" {
		t.Fatalf("committed = %q, want the recorded commit time", details.committed)
	}
}

func TestCollectBuildDetailsUsesBuildInfo(t *testing.T) {
	t.Parallel()

	details := collectBuildDetails(buildInfo("v0.4.0",
		debug.BuildSetting{Key: "vcs.revision", Value: "3425e45f5202b1c1d8c8ca26f86cc7f7b76b6ec4"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-08-13T07:19:26Z"},
	), true)

	if details.version != "v0.4.0" {
		t.Fatalf("version = %q, want the module version", details.version)
	}
	if details.commit != "3425e45f5202b1c1d8c8ca26f86cc7f7b76b6ec4" {
		t.Fatalf("commit = %q, want the recorded revision", details.commit)
	}
	if !details.modified {
		t.Fatal("dirty work tree not reported")
	}
	if details.goVersion != runtime.Version() || details.platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("toolchain details = %q %q, want the running toolchain", details.goVersion, details.platform)
	}
}

// A `go install module@version` build carries no VCS settings at all, which is
// the case most likely to print something empty or invented.
func TestCollectBuildDetailsWithoutVCSSettings(t *testing.T) {
	t.Parallel()

	out := &strings.Builder{}
	details := collectBuildDetails(buildInfo("v0.4.0"), true)
	if err := details.writeTo(out); err != nil {
		t.Fatalf("writeTo: %v", err)
	}

	want := "dirstat v0.4.0\ngo:       " + runtime.Version() +
		"\nplatform: " + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if out.String() != want {
		t.Fatalf("output =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestCollectBuildDetailsWithoutBuildInfo(t *testing.T) {
	t.Parallel()

	details := collectBuildDetails(nil, false)
	if details.version != unknownVersion {
		t.Fatalf("version = %q, want %q", details.version, unknownVersion)
	}
	if details.goVersion == "" || details.platform == "" {
		t.Fatal("toolchain details missing without build info")
	}
}

func TestWriteToAlignsAndMarksModified(t *testing.T) {
	t.Parallel()

	out := &strings.Builder{}
	details := collectBuildDetails(buildInfo("v1.2.3",
		debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
		debug.BuildSetting{Key: "vcs.time", Value: "2026-08-13T07:19:26Z"},
	), true)
	if err := details.writeTo(out); err != nil {
		t.Fatalf("writeTo: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if lines[0] != "dirstat v1.2.3" {
		t.Fatalf("first line = %q, want the program name and version", lines[0])
	}
	if lines[1] != "commit:    abc1234 (modified)" {
		t.Fatalf("commit line = %q", lines[1])
	}
	if lines[2] != "committed: 2026-08-13T07:19:26Z" {
		t.Fatalf("committed line = %q", lines[2])
	}
	for _, line := range lines[1:] {
		if !strings.Contains(line, ": ") {
			t.Fatalf("line %q is not a labelled field", line)
		}
	}
}

// The unstamped test binary exercises the real ReadBuildInfo path.
func TestPrintVersionAlwaysNamesAVersion(t *testing.T) {
	t.Parallel()

	out := &strings.Builder{}
	if err := printVersion(out); err != nil {
		t.Fatalf("printVersion: %v", err)
	}

	first, _, ok := strings.Cut(out.String(), "\n")
	if !ok {
		t.Fatalf("output %q is not newline terminated", out.String())
	}
	name, version, ok := strings.Cut(first, " ")
	if !ok || name != "dirstat" || strings.TrimSpace(version) == "" {
		t.Fatalf("first line = %q, want a program name and a non-empty version", first)
	}
	if !strings.Contains(out.String(), "platform: "+runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("output %q omits the platform", out.String())
	}
}
