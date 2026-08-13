// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package format

import "testing"

func TestBytes(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value uint64
		want  string
	}{
		{value: 0, want: "0B"},
		{value: 1023, want: "1023B"},
		{value: 1536, want: "1.5KiB"},
		{value: 1024 * 1024, want: "1.0MiB"},
		{value: 3 * 1024 * 1024 * 1024, want: "3.0GiB"},
	} {
		if got := Bytes(testCase.value); got != testCase.want {
			t.Errorf("Bytes(%d) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestCount(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value uint64
		want  string
	}{
		{value: 0, want: "0"},
		{value: 999, want: "999"},
		{value: 1_250, want: "1.2K"},
		{value: 1_250_000, want: "1.2M"},
		{value: 2_500_000_000, want: "2.5B"},
	} {
		if got := Count(testCase.value); got != testCase.want {
			t.Errorf("Count(%d) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestDisplayEscapesTerminalControls(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value string
		want  string
	}{
		{value: "plain", want: "plain"},
		{value: "line\n\x1b[2J", want: `line\n\x1b[2J`},
		{value: "with space", want: "with space"},
		{value: `quote"and\slash`, want: `quote\"and\\slash`},
		{value: "héllo", want: "héllo"},
	} {
		if got := Display(testCase.value); got != testCase.want {
			t.Errorf("Display(%q) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}
