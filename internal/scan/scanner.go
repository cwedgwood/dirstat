// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/cwedgwood/dirstat/internal/inventory"
)

const (
	syntheticRootID = "\x00"
	maxErrorSamples = 8
	readBatchSize   = 256

	// defaultRefreshInterval is how often directory updates are emitted. Every
	// state change is coalesced onto this tick, so a directory costs one event
	// per interval it changed in, whether it was queued, read, rolled up by a
	// descendant, or all three: a directory discovered and finished between
	// two ticks is reported once, in its final state. A human reads a display
	// a few times a second, so anything faster is built and discarded.
	defaultRefreshInterval = 100 * time.Millisecond
)

// State describes the current scan state of one directory.
type State string

const (
	StateQueued   State = "queued"
	StateScanning State = "scanning"
	StateComplete State = "complete"
	StatePartial  State = "partial"
	StateSkipped  State = "skipped-mount"
	StateAlias    State = "skipped-alias"
)

// Root is a validated scan root.
type Root struct {
	Path   string
	Device uint64
	Inode  uint64
	meta   metadata
}

// Options controls traversal.
type Options struct {
	Workers          int
	CrossFilesystems bool

	// FinalStatesOnly says the consumer will only ever look at a directory's
	// final roll-up, so the scanner emits nothing for a directory until it is
	// final, and drops the provisional bookkeeping that exists to feed those
	// intermediate updates. The terminal Done event is still sent.
	//
	// A ranked report discards every non-final node it is handed, which on a
	// 42.7K directory tree means building and sending 95K updates to print ten
	// lines. The zero value keeps every update, so a consumer that wants to
	// watch the tree fill in gets that without asking.
	FinalStatesOnly bool

	// refreshInterval overrides defaultRefreshInterval so tests can observe
	// coalesced updates without waiting on wall-clock time.
	refreshInterval time.Duration
}

func (r *directoryResult) addExpectedDirectory() {
	if !r.task.hasExpected {
		return
	}
	r.accumulator.AddObject(
		r.task.expected.key,
		r.task.expected.links,
		true,
		r.task.expected.allocated,
		r.task.expected.apparent,
	)
}

// Progress summarizes scanner activity.
type Progress struct {
	Discovered uint64
	Scanned    uint64
	Skipped    uint64
	Queued     uint64
	Active     uint64
	Errors     uint64

	// Elapsed is the time since the scan started, measured by the scanner
	// rather than by whoever is watching it, so it excludes process startup
	// and terminal setup. The terminal event is emitted as the scan ends, so
	// the last snapshot carries the total.
	Elapsed time.Duration
}

func metadataFromUnixStat(stat *unix.Stat_t) metadata {
	var allocated uint64
	if stat.Blocks > 0 {
		allocated = uint64(stat.Blocks) * 512
	}
	var apparent uint64
	if stat.Size > 0 {
		apparent = uint64(stat.Size)
	}
	return metadata{
		key: inventory.InodeKey{
			Device: uint64(stat.Dev),
			Inode:  stat.Ino,
		},
		links:     uint64(stat.Nlink),
		allocated: allocated,
		apparent:  apparent,
	}
}

// NodeUpdate is the latest state of one directory.
type NodeUpdate struct {
	ID           string
	ParentID     string
	Path         string
	Name         string
	Root         bool
	State        State
	Metrics      inventory.Metrics
	ErrorSamples []string
}

// Event carries a directory update or terminal scan state.
type Event struct {
	Node      *NodeUpdate
	Progress  Progress
	Done      bool
	Cancelled bool
	Summary   inventory.Metrics
}

// DefaultWorkers returns the number of directories to read concurrently.
//
// This deliberately does not scale with the CPU count. The work is dominated by
// waiting on filesystem metadata, not by computation, so the useful amount of
// concurrency is set by how many metadata operations the storage will overlap,
// which has nothing to do with how many cores are available to issue them.
//
// Measured on a 3.16M inode ZFS home directory, 8 cores / 16 threads, warm ARC:
//
//	workers   4    62.2s
//	workers   8    24.6s
//	workers  16    11.3s
//	workers  32     8.6s
//	workers  64     9.4s
//
// Scaling is super-linear to 16 and still improving at 32, which is what a
// latency-bound workload looks like; 64 gives it back. Tying this to
// GOMAXPROCS, as it used to, chose 8 workers on a 4-thread machine and was
// therefore about three times slower than necessary on identical storage.
//
// Override with --workers where the storage differs enough to matter.
func DefaultWorkers() int {
	return 32
}

// ResolveRoots normalizes, validates, and opens scan roots.
func ResolveRoots(paths []string) ([]Root, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}

	roots := make([]Root, 0, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", path, err)
		}
		absolute = filepath.Clean(absolute)
		if err := rejectSymlinkComponents(absolute); err != nil {
			return nil, err
		}

		info, err := os.Lstat(absolute)
		if err != nil {
			return nil, fmt.Errorf("inspect root %q: %w", absolute, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("root %q is a symbolic link; symbolic links are not followed", absolute)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("root %q is not a directory", absolute)
		}
		meta, err := metadataFromInfo(info)
		if err != nil {
			return nil, fmt.Errorf("inspect root %q: %w", absolute, err)
		}

		directoryFD, err := unix.Open(
			absolute,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		if err != nil {
			return nil, fmt.Errorf("open root %q: %w", absolute, err)
		}
		if err := unix.Close(directoryFD); err != nil {
			return nil, fmt.Errorf("close root %q: %w", absolute, err)
		}

		for _, existing := range roots {
			if pathsOverlap(existing.Path, absolute) {
				return nil, fmt.Errorf(
					"scan roots overlap: %q and %q",
					existing.Path,
					absolute,
				)
			}
			if existing.Device == meta.key.Device && existing.Inode == meta.key.Inode {
				return nil, fmt.Errorf(
					"scan roots refer to the same directory inode: %q and %q",
					existing.Path,
					absolute,
				)
			}
		}
		roots = append(roots, Root{
			Path:   absolute,
			Device: meta.key.Device,
			Inode:  meta.key.Inode,
			meta:   meta,
		})
	}
	return roots, nil
}

// Start begins a scan and closes the returned channel after completion or
// cancellation.
func Start(ctx context.Context, roots []Root, options Options) <-chan Event {
	if options.Workers <= 0 {
		options.Workers = DefaultWorkers()
	}
	if options.refreshInterval <= 0 {
		options.refreshInterval = defaultRefreshInterval
	}
	events := make(chan Event, 256)
	// Taken here rather than in the coordinator so the clock covers everything
	// the caller asked for, including scheduling that goroutine.
	started := time.Now()
	go coordinate(ctx, roots, options, events, started)
	return events
}

type metadata struct {
	key       inventory.InodeKey
	links     uint64
	allocated uint64
	apparent  uint64
}

type childDirectory struct {
	path    string
	name    string
	meta    metadata
	skipped bool
}

type directoryTask struct {
	path        string
	parentID    string
	rootDevice  uint64
	root        bool
	expected    metadata
	hasExpected bool
}

type directoryResult struct {
	task         directoryTask
	accumulator  inventory.Accumulator
	children     []childDirectory
	errorCount   uint64
	errorSamples []string
}

type directoryState struct {
	task        directoryTask
	accumulator inventory.Accumulator
	// provisional holds the totals of descendants that have been read but
	// whose accumulators have not yet been merged into this one. Hard links
	// are only deduplicated on merge, so this can over-count inodes and bytes;
	// it is emptied one descendant at a time as they finalize, which is why a
	// displayed roll-up can settle downwards but never has to grow at the end.
	provisional  inventory.Metrics
	pending      int
	scanned      bool
	published    bool
	finalized    bool
	skipState    State
	errorSamples []string

	// depth is the number of directories between this one and its scan root.
	// The flush orders by it, because a consumer that meets a directory
	// before its parent has nowhere to attach it.
	depth int

	// latest is the state the next flush should report, and flushIndex where
	// it is queued to be reported, or -1. Together they are what makes an
	// update coalesce: a directory that changes repeatedly between two ticks
	// overwrites latest and is emitted once.
	latest     State
	flushIndex int
}

func newDirectoryState(task directoryTask, depth int) *directoryState {
	return &directoryState{task: task, depth: depth, flushIndex: -1}
}

// pendingUpdate is one directory waiting to be reported. A live directory is
// held by pointer and rendered at flush time, so further changes cost nothing;
// a finalized one is held as the already-built update, because its state is
// dropped from the coordinator as soon as it merges into its parent.
type pendingUpdate struct {
	depth  int
	state  *directoryState
	update *NodeUpdate
}

func coordinate(
	ctx context.Context,
	roots []Root,
	options Options,
	events chan Event,
	started time.Time,
) {
	var (
		progress  Progress
		queue     []directoryTask
		active    int
		completed bool
		synthetic *directoryState
	)
	defer func() {
		if !completed && ctx.Err() != nil {
			summary := inventory.Metrics{}
			if synthetic != nil {
				summary = synthetic.accumulator.Metrics()
			}
			cancelledEvent := Event{
				Progress:  progressWithQueue(progress, started, len(queue), active),
				Done:      true,
				Cancelled: true,
				Summary:   summary,
			}
			select {
			case events <- cancelledEvent:
			default:
				// Reserve terminal-state delivery by dropping one stale
				// progress event if the consumer is behind.
				select {
				case <-events:
				default:
				}
				events <- cancelledEvent
			}
		}
		close(events)
	}()

	taskChannel := make(chan directoryTask)
	resultChannel := make(chan directoryResult)
	for range options.Workers {
		go worker(ctx, options, taskChannel, resultChannel)
	}
	if len(roots) == 0 {
		completed = true
		events <- Event{Progress: progressWithQueue(progress, started, 0, 0), Done: true}
		close(taskChannel)
		return
	}

	states := make(map[string]*directoryState)
	var pending []pendingUpdate
	visitedDirectories := make(map[inventory.InodeKey]string)
	// A consumer that only reads final roll-ups is not shown provisional
	// totals, so neither the intermediate events nor the ancestor arithmetic
	// behind them are worth doing.
	wantProgress := !options.FinalStatesOnly
	synthetic = newDirectoryState(directoryTask{path: syntheticRootID}, 0)
	synthetic.scanned = true
	synthetic.pending = len(roots)
	states[syntheticRootID] = synthetic

	queue = make([]directoryTask, 0, len(roots))
	progress = Progress{Discovered: uint64(len(roots))}
	for _, root := range roots {
		task := directoryTask{
			path:        root.Path,
			parentID:    syntheticRootID,
			rootDevice:  root.Device,
			root:        true,
			expected:    root.meta,
			hasExpected: true,
		}
		rootState := newDirectoryState(task, 0)
		rootState.latest = StateQueued
		states[root.Path] = rootState
		visitedDirectories[inventory.InodeKey{
			Device: root.Device,
			Inode:  root.Inode,
		}] = root.Path
		queue = append(queue, task)
		if !wantProgress {
			continue
		}
		// Roots are sent as they are queued rather than deferred: there are a
		// handful of them, it gives the display something to draw at once,
		// and it puts every root ahead of any event that depends on it.
		if !sendEvent(ctx, events, Event{
			Node:     nodeUpdate(rootState, StateQueued),
			Progress: progressWithQueue(progress, started, len(queue), 0),
		}) {
			close(taskChannel)
			return
		}
	}

	// markPending queues a directory to be reported by the next flush. A
	// directory already queued keeps its place, so repeated changes between
	// two ticks cost one event, not one each.
	markPending := func(state *directoryState) {
		if state.flushIndex >= 0 {
			return
		}
		state.flushIndex = len(pending)
		pending = append(pending, pendingUpdate{depth: state.depth, state: state})
	}

	// markFinal queues a directory's last state. The built update is held
	// rather than the state, because finalizing releases the state and its
	// accumulator; what is retained is one small update per directory that
	// finalized since the last tick, not one per directory scanned.
	markFinal := func(state *directoryState, finalState State) {
		update := pendingUpdate{depth: state.depth, update: nodeUpdate(state, finalState)}
		if state.flushIndex >= 0 {
			pending[state.flushIndex] = update
			state.flushIndex = -1
			return
		}
		pending = append(pending, update)
	}

	// adjustAncestors folds delta into the provisional total of every ancestor
	// and queues them for the next refresh tick. Trees are shallow relative to
	// their width, so walking the parent chain per directory costs a handful
	// of additions; recomputing roll-ups from the live state map instead would
	// scale with the size of the unfinished frontier.
	adjustAncestors := func(startID string, delta inventory.Metrics, add bool) {
		if !wantProgress {
			// Provisional totals are only ever read by an intermediate
			// update, and a final roll-up comes from the merged accumulators.
			return
		}
		for id := startID; id != ""; {
			state, exists := states[id]
			if !exists {
				return
			}
			if add {
				addMetrics(&state.provisional, delta)
			} else {
				subtractMetrics(&state.provisional, delta)
			}
			if id != syntheticRootID {
				markPending(state)
			}
			id = state.task.parentID
		}
	}

	// flush emits the latest state of every directory that changed since the
	// last tick, however many times it changed and however many descendants
	// contributed to the change.
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		// Shallowest first. A consumer links a directory to its parent when
		// it first meets it, so a child ahead of its parent would arrive
		// with nowhere to attach. Directories at the same depth are never
		// ancestors of each other, so their order does not matter.
		//
		// This leaves flushIndex stale, which is safe only because nothing
		// reads it before the loop below clears it: the coordinator is the
		// sole writer and does not run while a flush is in progress.
		slices.SortFunc(pending, func(left pendingUpdate, right pendingUpdate) int {
			return cmp.Compare(left.depth, right.depth)
		})
		for index := range pending {
			entry := &pending[index]
			update := entry.update
			if update == nil {
				update = nodeUpdate(entry.state, entry.state.latest)
				entry.state.flushIndex = -1
			}
			if !sendEvent(ctx, events, Event{
				Node:     update,
				Progress: progressWithQueue(progress, started, len(queue), active),
			}) {
				return false
			}
		}
		clear(pending)
		pending = pending[:0]
		return true
	}

	var finalize func(string) bool
	finalize = func(id string) bool {
		state, exists := states[id]
		if !exists {
			return true
		}
		if state.finalized {
			return true
		}
		state.finalized = true

		if id == syntheticRootID {
			// Every directory's final state is already queued by the time the
			// synthetic root finalizes, and the terminal event carries the
			// summary they add up to, so it has to leave last.
			if !flush() {
				return false
			}
			if !sendEvent(ctx, events, Event{
				Progress: progressWithQueue(progress, started, len(queue), active),
				Done:     true,
				Summary:  state.accumulator.Metrics(),
			}) {
				return false
			}
			completed = true
			return true
		}

		finalState := StateComplete
		if state.skipState != "" {
			finalState = state.skipState
		} else if state.accumulator.Metrics().Errors > 0 {
			finalState = StatePartial
		}
		if wantProgress {
			markFinal(state, finalState)
		} else if !sendEvent(ctx, events, Event{
			Node:     nodeUpdate(state, finalState),
			Progress: progressWithQueue(progress, started, len(queue), active),
		}) {
			return false
		}

		parent := states[state.task.parentID]
		if state.accumulator.Metrics().Errors > 0 {
			mergeErrorSamples(parent, state)
		}
		if state.published {
			adjustAncestors(state.task.parentID, state.accumulator.Metrics(), false)
		}
		before := parent.accumulator.Metrics()
		parent.accumulator.Merge(&state.accumulator)
		// The merge is where cross-subtree hard links are finally charged
		// once, so the parent's exact growth is usually less than what this
		// child had provisionally contributed to its own ancestors.
		adjustAncestors(
			parent.task.parentID,
			metricsDelta(before, parent.accumulator.Metrics()),
			true,
		)
		parent.pending--
		delete(states, id)

		if parent.scanned && parent.pending == 0 {
			return finalize(parent.task.path)
		}
		return true
	}

	// Nothing is deferred when only final states are wanted, so the loop is
	// left without a timer to wake it.
	var refreshTick <-chan time.Time
	if wantProgress {
		ticker := time.NewTicker(options.refreshInterval)
		defer ticker.Stop()
		refreshTick = ticker.C
	}

	for !completed {
		var nextTask directoryTask
		var sendTask chan<- directoryTask
		if len(queue) > 0 {
			nextTask = queue[0]
			sendTask = taskChannel
		}

		select {
		case <-ctx.Done():
			close(taskChannel)
			return

		case <-refreshTick:
			if !flush() {
				close(taskChannel)
				return
			}

		case sendTask <- nextTask:
			queue[0] = directoryTask{}
			queue = queue[1:]
			active++

		case result := <-resultChannel:
			active--
			progress.Scanned++
			progress.Errors += result.errorCount

			state := states[result.task.path]
			state.scanned = true
			state.accumulator = result.accumulator
			state.errorSamples = qualifyErrorSamples(state.task.path, result.errorSamples)
			state.pending = len(result.children)
			state.published = true
			adjustAncestors(state.task.parentID, state.accumulator.Metrics(), true)

			for _, child := range result.children {
				childTask := directoryTask{
					path:        child.path,
					parentID:    state.task.path,
					rootDevice:  state.task.rootDevice,
					expected:    child.meta,
					hasExpected: true,
				}
				childState := newDirectoryState(childTask, state.depth+1)
				childState.latest = StateQueued
				skipState, existingPath := classifyDirectory(child, visitedDirectories)
				childState.skipState = skipState
				if skipState == StateAlias {
					childState.errorSamples = []string{
						fmt.Sprintf("directory inode already inventoried at %s", existingPath),
					}
				}
				states[child.path] = childState
				progress.Discovered++

				if childState.skipState != "" {
					childState.scanned = true
					if childState.skipState == StateAlias {
						childState.accumulator.AddDirectoryAlias()
					} else {
						childState.accumulator.AddObject(
							child.meta.key,
							child.meta.links,
							true,
							child.meta.allocated,
							child.meta.apparent,
						)
					}
					progress.Skipped++
					continue
				}

				queue = append(queue, childTask)
				if wantProgress {
					markPending(childState)
				}
			}

			if wantProgress {
				state.latest = StateScanning
				markPending(state)
			}

			for _, child := range result.children {
				if states[child.path].skipState != "" && !finalize(child.path) {
					close(taskChannel)
					return
				}
			}
			if state.pending == 0 && !finalize(state.task.path) {
				close(taskChannel)
				return
			}
		}
	}

	close(taskChannel)
}

func worker(
	ctx context.Context,
	options Options,
	tasks <-chan directoryTask,
	results chan<- directoryResult,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case task, ok := <-tasks:
			if !ok {
				return
			}
			result := scanDirectory(ctx, task, options)
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}
		}
	}
}

func scanDirectory(
	ctx context.Context,
	task directoryTask,
	options Options,
) directoryResult {
	result := directoryResult{task: task}

	directoryFD, err := unix.Open(
		task.path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		result.addExpectedDirectory()
		result.addError(fmt.Sprintf("open directory: %v", err))
		result.accumulator.AddErrors(result.errorCount)
		return result
	}
	directory := os.NewFile(uintptr(directoryFD), task.path)
	if directory == nil {
		unix.Close(directoryFD)
		result.addExpectedDirectory()
		result.addError("open directory: could not wrap file descriptor")
		result.accumulator.AddErrors(result.errorCount)
		return result
	}
	defer directory.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(directoryFD, &stat); err != nil {
		result.addExpectedDirectory()
		result.addError(fmt.Sprintf("inspect opened directory: %v", err))
		result.accumulator.AddErrors(result.errorCount)
		return result
	}
	meta := metadataFromUnixStat(&stat)
	if task.hasExpected && meta.key != task.expected.key {
		result.addExpectedDirectory()
		result.addError(fmt.Sprintf(
			"directory changed before scan: expected device %d inode %d, opened device %d inode %d",
			task.expected.key.Device,
			task.expected.key.Inode,
			meta.key.Device,
			meta.key.Inode,
		))
		result.accumulator.AddErrors(result.errorCount)
		return result
	}
	if !options.CrossFilesystems && meta.key.Device != task.rootDevice {
		result.addExpectedDirectory()
		result.addError(fmt.Sprintf(
			"directory moved to filesystem device %d before scan; root device is %d",
			meta.key.Device,
			task.rootDevice,
		))
		result.accumulator.AddErrors(result.errorCount)
		return result
	}
	result.accumulator.AddObject(
		meta.key,
		meta.links,
		true,
		meta.allocated,
		meta.apparent,
	)

	for {
		if err := ctx.Err(); err != nil {
			return result
		}

		entries, readErr := directory.ReadDir(readBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result
			}

			var entryStat unix.Stat_t
			if statErr := unix.Fstatat(
				directoryFD,
				entry.Name(),
				&entryStat,
				unix.AT_SYMLINK_NOFOLLOW,
			); statErr != nil {
				result.addError(fmt.Sprintf("%s: %v", entry.Name(), statErr))
				continue
			}
			entryMeta := metadataFromUnixStat(&entryStat)

			if entryStat.Mode&unix.S_IFMT == unix.S_IFDIR {
				result.children = append(result.children, childDirectory{
					path: filepath.Join(task.path, entry.Name()),
					name: entry.Name(),
					meta: entryMeta,
					skipped: !options.CrossFilesystems &&
						entryMeta.key.Device != task.rootDevice,
				})
				continue
			}

			result.accumulator.AddObject(
				entryMeta.key,
				entryMeta.links,
				false,
				entryMeta.allocated,
				entryMeta.apparent,
			)
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			result.addError(fmt.Sprintf("read directory: %v", readErr))
			break
		}
	}

	result.accumulator.AddErrors(result.errorCount)
	return result
}

func (r *directoryResult) addError(message string) {
	r.errorCount++
	if len(r.errorSamples) < maxErrorSamples {
		r.errorSamples = append(r.errorSamples, message)
	}
}

func nodeUpdate(state *directoryState, nodeState State) *NodeUpdate {
	name := filepath.Base(state.task.path)
	if state.task.path == string(filepath.Separator) {
		name = state.task.path
	}
	metrics := state.accumulator.Metrics()
	addMetrics(&metrics, state.provisional)
	return &NodeUpdate{
		ID:           state.task.path,
		ParentID:     state.task.parentID,
		Path:         state.task.path,
		Name:         name,
		Root:         state.task.root,
		State:        nodeState,
		Metrics:      metrics,
		ErrorSamples: append([]string(nil), state.errorSamples...),
	}
}

func addMetrics(target *inventory.Metrics, delta inventory.Metrics) {
	target.Files += delta.Files
	target.Directories += delta.Directories
	target.Inodes += delta.Inodes
	target.Allocated += delta.Allocated
	target.Apparent += delta.Apparent
	target.Errors += delta.Errors
}

func subtractMetrics(target *inventory.Metrics, delta inventory.Metrics) {
	target.Files -= delta.Files
	target.Directories -= delta.Directories
	target.Inodes -= delta.Inodes
	target.Allocated -= delta.Allocated
	target.Apparent -= delta.Apparent
	target.Errors -= delta.Errors
}

// metricsDelta reports how much an accumulator grew. Merge never reduces a
// field, so the result is the amount an ancestor must now account for.
func metricsDelta(before inventory.Metrics, after inventory.Metrics) inventory.Metrics {
	return inventory.Metrics{
		Files:       after.Files - before.Files,
		Directories: after.Directories - before.Directories,
		Inodes:      after.Inodes - before.Inodes,
		Allocated:   after.Allocated - before.Allocated,
		Apparent:    after.Apparent - before.Apparent,
		Errors:      after.Errors - before.Errors,
	}
}

func sendEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func progressWithQueue(
	progress Progress,
	started time.Time,
	queued int,
	active int,
) Progress {
	progress.Queued = uint64(queued)
	progress.Active = uint64(active)
	progress.Elapsed = time.Since(started)
	return progress
}

func metadataFromInfo(info os.FileInfo) (metadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return metadata{}, fmt.Errorf("unsupported stat data for %q", info.Name())
	}

	var allocated uint64
	if stat.Blocks > 0 {
		allocated = uint64(stat.Blocks) * 512
	}
	var apparent uint64
	if stat.Size > 0 {
		apparent = uint64(stat.Size)
	}
	return metadata{
		key: inventory.InodeKey{
			Device: uint64(stat.Dev),
			Inode:  stat.Ino,
		},
		links:     uint64(stat.Nlink),
		allocated: allocated,
		apparent:  apparent,
	}, nil
}

func pathsOverlap(left string, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func rejectSymlinkComponents(path string) error {
	current := string(filepath.Separator)
	for component := range strings.SplitSeq(
		strings.TrimPrefix(path, string(filepath.Separator)),
		string(filepath.Separator),
	) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect root component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"root %q contains symbolic-link component %q; symbolic links are not followed",
				path,
				current,
			)
		}
	}
	return nil
}

func classifyDirectory(
	child childDirectory,
	visited map[inventory.InodeKey]string,
) (State, string) {
	if existingPath, seen := visited[child.meta.key]; seen {
		return StateAlias, existingPath
	}
	visited[child.meta.key] = child.path
	if child.skipped {
		return StateSkipped, ""
	}
	return "", ""
}

func qualifyErrorSamples(path string, samples []string) []string {
	qualified := make([]string, len(samples))
	for index, sample := range samples {
		qualified[index] = path + ": " + sample
	}
	return qualified
}

func mergeErrorSamples(parent *directoryState, child *directoryState) {
	for _, sample := range child.errorSamples {
		if len(parent.errorSamples) >= maxErrorSamples {
			return
		}
		parent.errorSamples = append(parent.errorSamples, sample)
	}
}

func pathContains(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
			!filepath.IsAbs(relative))
}
