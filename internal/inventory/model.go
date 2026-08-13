// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package inventory

// Metrics contains recursively rolled-up inventory values.
type Metrics struct {
	Files       uint64
	Directories uint64
	Inodes      uint64
	Allocated   uint64
	Apparent    uint64
	Errors      uint64
}

// InodeKey uniquely identifies a filesystem object on Linux.
type InodeKey struct {
	Device uint64
	Inode  uint64
}

type inodeValue struct {
	allocated uint64
	apparent  uint64
}

// Accumulator builds exact per-subtree totals while retaining identities only
// for non-directory objects that may have multiple hard links.
type Accumulator struct {
	metrics   Metrics
	hardLinks map[InodeKey]inodeValue
}

// AddObject adds one directory entry to the accumulator.
func (a *Accumulator) AddObject(
	key InodeKey,
	linkCount uint64,
	isDirectory bool,
	allocated uint64,
	apparent uint64,
) {
	if isDirectory {
		a.metrics.Directories++
		a.metrics.Inodes++
		a.metrics.Allocated += allocated
		a.metrics.Apparent += apparent
		return
	}

	a.metrics.Files++
	if linkCount <= 1 {
		a.metrics.Inodes++
		a.metrics.Allocated += allocated
		a.metrics.Apparent += apparent
		return
	}

	if a.hardLinks == nil {
		a.hardLinks = make(map[InodeKey]inodeValue)
	}
	value := inodeValue{allocated: allocated, apparent: apparent}
	if existing, ok := a.hardLinks[key]; ok {
		a.replaceWithLargerValue(key, existing, value)
		return
	}
	a.hardLinks[key] = value
	a.metrics.Inodes++
	a.metrics.Allocated += allocated
	a.metrics.Apparent += apparent
}

// AddErrors adds errors encountered directly within this subtree.
func (a *Accumulator) AddErrors(count uint64) {
	a.metrics.Errors += count
}

// AddDirectoryAlias counts another directory path without charging the already
// inventoried inode or its storage again.
func (a *Accumulator) AddDirectoryAlias() {
	a.metrics.Directories++
}

// Merge incorporates a completed child subtree and releases its merge-only
// hard-link state.
func (a *Accumulator) Merge(child *Accumulator) {
	a.metrics.Files += child.metrics.Files
	a.metrics.Directories += child.metrics.Directories
	a.metrics.Inodes += child.metrics.Inodes
	a.metrics.Allocated += child.metrics.Allocated
	a.metrics.Apparent += child.metrics.Apparent
	a.metrics.Errors += child.metrics.Errors

	left := a.hardLinks
	right := child.hardLinks
	if len(left) < len(right) {
		left, right = right, left
	}
	if left == nil && len(right) > 0 {
		left = make(map[InodeKey]inodeValue, len(right))
	}
	for key, value := range right {
		existing, duplicate := left[key]
		if !duplicate {
			left[key] = value
			continue
		}

		a.metrics.Inodes--
		a.metrics.Allocated -= min(existing.allocated, value.allocated)
		a.metrics.Apparent -= min(existing.apparent, value.apparent)
		left[key] = inodeValue{
			allocated: max(existing.allocated, value.allocated),
			apparent:  max(existing.apparent, value.apparent),
		}
	}
	a.hardLinks = left
	child.hardLinks = nil
}

// Metrics returns a copy of the accumulated values.
func (a *Accumulator) Metrics() Metrics {
	return a.metrics
}

func (a *Accumulator) replaceWithLargerValue(
	key InodeKey,
	existing inodeValue,
	replacement inodeValue,
) {
	if replacement.allocated > existing.allocated {
		a.metrics.Allocated += replacement.allocated - existing.allocated
		existing.allocated = replacement.allocated
	}
	if replacement.apparent > existing.apparent {
		a.metrics.Apparent += replacement.apparent - existing.apparent
		existing.apparent = replacement.apparent
	}
	a.hardLinks[key] = existing
}
