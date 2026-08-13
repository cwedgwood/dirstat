// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package inventory

import "testing"

func TestParseSortFieldRoundTrips(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		want SortField
		fail bool
	}{
		{name: "allocated", want: SortAllocated},
		{name: "inodes", want: SortInodes},
		{name: "files", want: SortFiles},
		{name: "apparent", want: SortApparent},
		{name: "name", want: SortName},
		{name: "Allocated", fail: true},
		{name: "", fail: true},
		{name: "size", fail: true},
	} {
		got, err := ParseSortField(testCase.name)
		if testCase.fail {
			if err == nil {
				t.Errorf("ParseSortField(%q) accepted an unknown field", testCase.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSortField(%q): %v", testCase.name, err)
			continue
		}
		if got != testCase.want || got.String() != testCase.name {
			t.Errorf("ParseSortField(%q) = %v (%q)", testCase.name, got, got.String())
		}
	}
}

func TestSortFieldCompare(t *testing.T) {
	t.Parallel()

	small := Metrics{Files: 1, Inodes: 2, Allocated: 3, Apparent: 4}
	large := Metrics{Files: 2, Inodes: 3, Allocated: 4, Apparent: 5}

	for _, field := range []SortField{
		SortAllocated,
		SortInodes,
		SortFiles,
		SortApparent,
	} {
		if got := field.Compare(small, large); got >= 0 {
			t.Errorf("%v.Compare(small, large) = %d, want negative", field, got)
		}
		if got := field.Compare(large, small); got <= 0 {
			t.Errorf("%v.Compare(large, small) = %d, want positive", field, got)
		}
		if got := field.Compare(small, small); got != 0 {
			t.Errorf("%v.Compare(small, small) = %d, want 0", field, got)
		}
	}
	if got := SortName.Compare(small, large); got != 0 {
		t.Errorf("SortName.Compare = %d, want 0 so callers break the tie by name", got)
	}
}
