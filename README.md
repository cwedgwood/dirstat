# dirstat

```sh
go install github.com/cwedgwood/dirstat/cmd/dirstat@latest
dirstat ~
dirstat --top 10 --sort inodes ~
```

`dirstat` is a read-only Linux terminal application for finding directory trees that consume large
numbers of file entries, unique inodes, or disk blocks. It scans incrementally and displays
recursively rolled-up totals in an interactive tree, or prints a ranked list and exits.

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

## Version

```sh
dirstat --version
```

`--version` prints what the binary knows about its own build and exits without
scanning anything, so it is safe to redirect or pipe:

```
dirstat v0.4.0
commit:    3425e45f5202b1c1d8c8ca26f86cc7f7b76b6ec4
committed: 2026-08-13T07:19:26Z
go:        go1.25.0
platform:  linux/amd64
```

A released binary reports the tag it was built from and that tag's commit. A `go install` build
reports the module version the `go` command resolved, and has no commit or commit time to report, so
those lines are omitted rather than filled in with something invented. A build from a working tree
reports whatever the toolchain recorded, with `(modified)` after the commit when the tree had
uncommitted changes.

## Run

```sh
dirstat
dirstat /path/to/inspect
dirstat /first/root /second/root
dirstat --workers 16 /path/to/inspect
dirstat --sort inodes /path/to/inspect
dirstat --cross-filesystems /
```

With no path, the current working directory is scanned. Scan roots must be directories and may not
overlap. Without `--top` the interactive tree is shown; see [Ranked list](#ranked-list) for
non-interactive output. `dirstat --help` lists every flag.

`--sort` sets the order the tree opens in, using the same field names the ranked list and the
interactive `s` menu accept. Direction follows the field, so a quantity opens largest first and
`--sort name` opens A to Z, matching how `--top` orders the same fields; `O` still reverses either.
`--exact` applies to `--top` output only, because it is about printing unscaled numbers and the tree
never does that.

By default, traversal stays on the filesystem containing each scan root.  Mounted directories are
counted and marked `[m]`, but their contents are not read. `--cross-filesystems` disables that
boundary.

Symbolic links are never followed. Symlinks, devices, pipes, sockets, and other special files are
counted as entries and inodes.

Directory identities are tracked during a scan. If the same directory inode is encountered again
through a bind alias or another unusual namespace path, the alias is counted, marked `[a]`, and not
traversed again. This prevents cycles and duplicated subtree totals. An alias row is a reference to
the first path: its duplicate inode and byte totals remain owned by that canonical row.

## Ranked list

```sh
dirstat --top 10 ~
dirstat --top 10 --sort inodes ~
dirstat --top 0 --sort files --exact ~ > inventory.txt
```

`--top` scans to completion without starting the interactive tree, prints a flat ranked list of the
directories holding the most of one metric, and exits. `--top 0` prints every directory. The output
is plain text with no color or terminal styling wherever it is written, so it can be redirected,
piped, diffed between runs, or fed to `grep` and `awk`.

```
root /home/cw/wk/dirstat  dirs 162  files 234  inodes 396  alloc 10.4MiB  apparent 15.2MiB  errors 0
    FILES    INODES      ALLOC   APPARENT  STATE   PATH
      208       360     1.6MiB   213.9KiB  ok      .git
      147       262     1.2MiB   166.1KiB  ok      .git/objects
       24        37   100.0KiB     9.6KiB  ok      .git/worktrees
```

The first line names the scan root and its whole-tree totals. Ranked paths below it are relative to
that root. Each scan root gets its own section, separated by a blank line, and its own `--top`
budget, so comparing two trees is not dominated by the larger one.

Every row is the rolled-up total of that whole subtree, the same number the interactive tree shows,
so a parent always outranks its children. This is deliberate: a subtree is the thing you move,
archive, or delete. The root itself is not ranked, because it would always come first and waste a
row.

`--sort` accepts `allocated` (the default), `inodes`, `files`, `apparent`, and `name`, the same keys
the interactive `s` menu offers. Numeric keys rank largest first and break ties by path; `name`
orders by path ascending. `allocated` is the default because `apparent` on a real tree is dominated
by sparse files, which answers what claims to be big rather than what is consuming disk.

`STATE` is `ok`, `partial` for a subtree that completed with errors, `mount` for a mount point that
was not descended into, or `alias` for a repeated directory inode.

Values use the same human-readable units as the interactive tree. `--exact` prints unscaled entry
counts and byte counts instead.

Scan errors do not fail the run: totals are printed, and an error count with up to eight samples is
written to standard error. The exit status is 0 when the scan completed, 1 when it did not, and 2
for a usage error.

`--workers` and `--cross-filesystems` apply as they do interactively. `--no-color` is accepted but
has no effect here, because ranked output is never styled. Directory names are escaped, so a name
containing newlines or escape sequences cannot forge a row or reprogram the terminal.

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

A directory's roll-up includes provisional totals from subtrees that are still being read, so it
climbs throughout the scan instead of jumping at the end. Hard links are only deduplicated where two
subtrees meet, which cannot happen until both are complete, so a provisional row can over-count a
shared inode and settle to a smaller value when it becomes final. Final totals are exact.

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

Ties break alphabetically whichever way the metric runs, as the ranked list does, so reversing the
sort does not also shuffle rows that are equal under it.

A rescan keeps the sort, filter, column mode, details setting, expanded directories, and selected
directory, so checking whether a deletion freed the expected space does not mean navigating back
down from the root. Restoring the selection follows the new scan as it discovers directories. If the
selected directory is gone, the deepest surviving parent of it is selected instead.

The application does not move, remove, or otherwise modify filesystem content.

## License

Apache-2.0. See [LICENSE](LICENSE).
