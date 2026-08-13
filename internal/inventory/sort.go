// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package inventory

import "fmt"

// SortField selects the rolled-up metric that orders directories.
type SortField int

// Ordering of the fields is also the order the interactive picker cycles them.
const (
	SortAllocated SortField = iota
	SortInodes
	SortFiles
	SortApparent
	SortName
)

// String returns the name accepted by the --sort flag and shown in the
// interactive sort picker.
func (f SortField) String() string {
	switch f {
	case SortAllocated:
		return "allocated"
	case SortInodes:
		return "inodes"
	case SortFiles:
		return "files"
	case SortApparent:
		return "apparent"
	case SortName:
		return "name"
	default:
		return "unknown"
	}
}

// ParseSortField resolves a sort field by name.
func ParseSortField(name string) (SortField, error) {
	for _, field := range []SortField{
		SortAllocated,
		SortInodes,
		SortFiles,
		SortApparent,
		SortName,
	} {
		if field.String() == name {
			return field, nil
		}
	}
	return SortAllocated, fmt.Errorf(
		"unknown sort field %q; want allocated, inodes, files, apparent, or name",
		name,
	)
}

// Compare orders two rolled-up metric sets by the field, returning a negative
// number when left holds less of it than right. SortName always compares equal
// because names are not part of Metrics; callers already break ties by name, so
// that tiebreak becomes the ordering.
func (f SortField) Compare(left Metrics, right Metrics) int {
	switch f {
	case SortAllocated:
		return compareUint64(left.Allocated, right.Allocated)
	case SortInodes:
		return compareUint64(left.Inodes, right.Inodes)
	case SortFiles:
		return compareUint64(left.Files, right.Files)
	case SortApparent:
		return compareUint64(left.Apparent, right.Apparent)
	default:
		return 0
	}
}

func compareUint64(left uint64, right uint64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
