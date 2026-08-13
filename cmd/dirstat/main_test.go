// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestUsageDocumentsEveryFlag(t *testing.T) {
	t.Parallel()

	out := &strings.Builder{}
	var opts options
	newFlagSet("dirstat", out, &opts).Usage()

	for _, name := range []string{
		"-workers", "-cross-filesystems", "-no-color", "-top", "-sort", "-exact", "-version",
	} {
		if !strings.Contains(out.String(), "  "+name) {
			t.Errorf("usage does not document %s", name)
		}
	}
	for _, phrase := range []string{"Usage:", "interactive tree", "exit status"} {
		if !strings.Contains(strings.ToLower(out.String()), strings.ToLower(phrase)) {
			t.Errorf("usage does not mention %q", phrase)
		}
	}
}
