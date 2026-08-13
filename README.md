# dirstat

```sh
go install github.com/cwedgwood/dirstat/cmd/dirstat@latest
dirstat ~
```

`dirstat` is a read-only Linux terminal application for finding directory trees that consume large
numbers of file entries, unique inodes, or disk blocks. It scans incrementally and displays
recursively rolled-up totals in an interactive tree.

`go install` needs Go 1.25 or newer and puts `dirstat` in `$(go env GOPATH)/bin`, which must be on
your `$PATH`.

## Other ways to install

Download a binary from the [releases page](https://github.com/cwedgwood/dirstat/releases): each
tagged release publishes static Linux binaries for amd64, arm64, 386, and riscv64 with SHA-256
checksums.

Or build from source:

```sh
git clone https://github.com/cwedgwood/dirstat
cd dirstat
make build
```

This creates `./dirstat`.

## Run

```sh
dirstat
dirstat /path/to/inspect
dirstat /first/root /second/root
dirstat --workers 16 /path/to/inspect
dirstat --cross-filesystems /
```

With no path, the current working directory is scanned. Scan roots must be directories and may not
overlap.

By default, traversal stays on the filesystem containing each scan root.  Mounted directories are
counted and marked `[m]`, but their contents are not read. `--cross-filesystems` disables that
boundary.

Symbolic links are never followed. Symlinks, devices, pipes, sockets, and other special files are
counted as entries and inodes.

Directory identities are tracked during a scan. If the same directory inode is encountered again
through a bind alias or another unusual namespace path, the alias is counted, marked `[a]`, and not
traversed again. This prevents cycles and duplicated subtree totals. An alias row is a reference to
the first path: its duplicate inode and byte totals remain owned by that canonical row.

## Metrics

- **Files** counts non-directory entries. Each hard-link name counts as a file entry.
- **Inodes** counts distinct `(device, inode)` objects, including directories and special files.
- **Alloc** is allocated storage from Linux `st_blocks`, deduplicated by inode within each displayed
  subtree.
- **Apparent** is logical `st_size`, also deduplicated by inode.

When a changing hard-linked file is observed with different sizes through different names, the
roll-up uses the largest allocated and apparent values seen during that scan. A live scan is not an
atomic filesystem snapshot; files created, removed, relinked, or replaced during traversal can make
partial totals change or produce visible errors.

Rows whose roll-ups are still provisional show `updating...` in the **Status** column. The text
disappears when the row is final. A trailing `!` means the subtree completed with one or more
errors. Press `d` to inspect error samples and exact totals.

## Keys

| Key                     | Action                                                        |
|-------------------------|---------------------------------------------------------------|
| Arrow keys or `j`/`k`   | Move                                                          |
| `Page Up` / `Page Down` | Move by one visible page                                      |
| `Enter`, right, or `l`  | Expand                                                        |
| Left or `h`             | Collapse or select parent                                     |
| `s`                     | Choose allocated, inode, file, apparent-size, or name sorting |
| `o`                     | Cycle sort field                                              |
| `O`                     | Reverse sort direction                                        |
| `/`                     | Filter by directory name or path                              |
| `c`                     | Toggle compact/full columns                                   |
| `d`                     | Toggle details and error samples                              |
| `r`                     | Rescan                                                        |
| `q`, `Esc`, or `Ctrl+C` | Exit                                                          |

The application does not move, remove, or otherwise modify filesystem content.

## License

Apache-2.0. See [LICENSE](LICENSE).
