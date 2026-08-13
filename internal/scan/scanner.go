// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/cwedgwood/dirstat/internal/inventory"
)

const (
	syntheticRootID = "\x00"
	maxErrorSamples = 8
	readBatchSize   = 256
	updateEvery     = 64
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

// DefaultWorkers returns a conservative amount of metadata concurrency.
func DefaultWorkers() int {
	return min(32, max(4, runtime.GOMAXPROCS(0)*2))
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
	events := make(chan Event, 256)
	go coordinate(ctx, roots, options, events)
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
	task         directoryTask
	accumulator  inventory.Accumulator
	pending      int
	scanned      bool
	finalized    bool
	skipState    State
	mergeCount   int
	errorSamples []string
}

func coordinate(
	ctx context.Context,
	roots []Root,
	options Options,
	events chan Event,
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
				Progress:  progressWithQueue(progress, len(queue), active),
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
		events <- Event{Done: true}
		close(taskChannel)
		return
	}

	states := make(map[string]*directoryState)
	visitedDirectories := make(map[inventory.InodeKey]string)
	synthetic = &directoryState{
		task:    directoryTask{path: syntheticRootID},
		scanned: true,
		pending: len(roots),
	}
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
		states[root.Path] = &directoryState{task: task}
		visitedDirectories[inventory.InodeKey{
			Device: root.Device,
			Inode:  root.Inode,
		}] = root.Path
		queue = append(queue, task)
		if !sendEvent(ctx, events, Event{
			Node:     nodeUpdate(states[root.Path], StateQueued),
			Progress: progressWithQueue(progress, len(queue), 0),
		}) {
			close(taskChannel)
			return
		}
	}

	emitNode := func(state *directoryState, nodeState State) bool {
		return sendEvent(ctx, events, Event{
			Node:     nodeUpdate(state, nodeState),
			Progress: progressWithQueue(progress, len(queue), active),
		})
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
			if !sendEvent(ctx, events, Event{
				Progress: progressWithQueue(progress, len(queue), active),
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
		if !emitNode(state, finalState) {
			return false
		}

		parent := states[state.task.parentID]
		if state.accumulator.Metrics().Errors > 0 {
			mergeErrorSamples(parent, state)
		}
		parent.accumulator.Merge(&state.accumulator)
		parent.pending--
		parent.mergeCount++
		delete(states, id)

		if parent.task.path != syntheticRootID &&
			parent.pending > 0 &&
			parent.mergeCount%updateEvery == 0 {
			if !emitNode(parent, StateScanning) {
				return false
			}
		}
		if parent.scanned && parent.pending == 0 {
			return finalize(parent.task.path)
		}
		return true
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

			for _, child := range result.children {
				childTask := directoryTask{
					path:        child.path,
					parentID:    state.task.path,
					rootDevice:  state.task.rootDevice,
					expected:    child.meta,
					hasExpected: true,
				}
				childState := &directoryState{
					task: childTask,
				}
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
				if !emitNode(childState, StateQueued) {
					close(taskChannel)
					return
				}
			}

			if !emitNode(state, StateScanning) {
				close(taskChannel)
				return
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
	return &NodeUpdate{
		ID:           state.task.path,
		ParentID:     state.task.parentID,
		Path:         state.task.path,
		Name:         name,
		Root:         state.task.root,
		State:        nodeState,
		Metrics:      state.accumulator.Metrics(),
		ErrorSamples: append([]string(nil), state.errorSamples...),
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

func progressWithQueue(progress Progress, queued int, active int) Progress {
	progress.Queued = uint64(queued)
	progress.Active = uint64(active)
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
