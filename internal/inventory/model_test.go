// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package inventory

import "testing"

func TestAccumulatorDeduplicatesHardLinks(t *testing.T) {
	t.Parallel()

	key := InodeKey{Device: 1, Inode: 42}
	var left Accumulator
	left.AddObject(InodeKey{Device: 1, Inode: 1}, 2, true, 4096, 128)
	left.AddObject(key, 2, false, 8192, 1000)

	var right Accumulator
	right.AddObject(InodeKey{Device: 1, Inode: 2}, 2, true, 4096, 128)
	right.AddObject(key, 2, false, 8192, 1000)

	left.Merge(&right)
	got := left.Metrics()
	if got.Files != 2 {
		t.Fatalf("files = %d, want 2 directory entries", got.Files)
	}
	if got.Directories != 2 {
		t.Fatalf("directories = %d, want 2", got.Directories)
	}
	if got.Inodes != 3 {
		t.Fatalf("inodes = %d, want 3 unique objects", got.Inodes)
	}
	if got.Allocated != 16_384 {
		t.Fatalf("allocated = %d, want 16384", got.Allocated)
	}
	if got.Apparent != 1256 {
		t.Fatalf("apparent = %d, want 1256", got.Apparent)
	}
	if right.hardLinks != nil {
		t.Fatal("child retained merge-only hard-link state")
	}
}

func TestAccumulatorDeduplicatesLinksWithinDirectory(t *testing.T) {
	t.Parallel()

	key := InodeKey{Device: 7, Inode: 9}
	var accumulator Accumulator
	accumulator.AddObject(key, 3, false, 4096, 12)
	accumulator.AddObject(key, 3, false, 4096, 12)
	accumulator.AddObject(key, 3, false, 8192, 20)

	got := accumulator.Metrics()
	if got.Files != 3 {
		t.Fatalf("files = %d, want 3", got.Files)
	}
	if got.Inodes != 1 {
		t.Fatalf("inodes = %d, want 1", got.Inodes)
	}
	if got.Allocated != 8192 {
		t.Fatalf("allocated = %d, want largest observed value 8192", got.Allocated)
	}
	if got.Apparent != 20 {
		t.Fatalf("apparent = %d, want largest observed value 20", got.Apparent)
	}
}

func TestAccumulatorMergesSmallMapIntoLargeMap(t *testing.T) {
	t.Parallel()

	var parent Accumulator
	parent.AddObject(InodeKey{Device: 1, Inode: 1}, 2, false, 1, 1)

	var child Accumulator
	child.AddObject(InodeKey{Device: 1, Inode: 1}, 2, false, 1, 1)
	child.AddObject(InodeKey{Device: 1, Inode: 2}, 2, false, 2, 2)
	child.AddObject(InodeKey{Device: 1, Inode: 3}, 2, false, 3, 3)

	parent.Merge(&child)
	got := parent.Metrics()
	if got.Files != 4 || got.Inodes != 3 || got.Allocated != 6 {
		t.Fatalf("unexpected metrics after small-to-large merge: %+v", got)
	}
}

func TestAccumulatorDirectoryAliasDoesNotDuplicateInode(t *testing.T) {
	t.Parallel()

	var accumulator Accumulator
	accumulator.AddObject(InodeKey{Device: 1, Inode: 1}, 2, true, 4096, 128)
	accumulator.AddDirectoryAlias()

	got := accumulator.Metrics()
	if got.Directories != 2 || got.Inodes != 1 || got.Allocated != 4096 {
		t.Fatalf("unexpected alias metrics: %+v", got)
	}
}

func BenchmarkAccumulatorHardLinkMerge(b *testing.B) {
	for range b.N {
		var parent Accumulator
		for childIndex := range 100 {
			var child Accumulator
			for inodeIndex := range 100 {
				child.AddObject(
					InodeKey{
						Device: 1,
						Inode:  uint64(inodeIndex + childIndex*50),
					},
					2,
					false,
					4096,
					1024,
				)
			}
			parent.Merge(&child)
		}
	}
}
