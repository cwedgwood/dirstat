// Copyright (C) 2026 Chris Wedgwood
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/cwedgwood/dirstat/internal/format"
	"github.com/cwedgwood/dirstat/internal/inventory"
	"github.com/cwedgwood/dirstat/internal/scan"
)

type treeNode struct {
	id           string
	parentID     string
	path         string
	name         string
	lowerName    string
	state        scan.State
	metrics      inventory.Metrics
	errorSamples []string
	children     []*treeNode
}

type row struct {
	id    string
	depth int
}

type scanEventMsg struct {
	event   scan.Event
	channel <-chan scan.Event
	ok      bool
}

// Options controls presentation.
type Options struct {
	NoColor bool
}

// Model is the Bubble Tea application model.
type Model struct {
	baseContext context.Context
	roots       []scan.Root
	scanOptions scan.Options
	options     Options

	scanContext context.Context
	cancelScan  context.CancelFunc
	events      <-chan scan.Event

	nodes    map[string]*treeNode
	rootIDs  []string
	expanded map[string]bool

	progress  scan.Progress
	summary   inventory.Metrics
	done      bool
	cancelled bool

	width  int
	height int
	cursor int
	offset int

	sortField  inventory.SortField
	sortChoice inventory.SortField
	descending bool
	selectSort bool
	filter     string
	filtering  bool
	compact    bool
	details    bool

	selectedStyle lipgloss.Style
	activeStyle   lipgloss.Style
	warningStyle  lipgloss.Style
}

// New creates and starts an inventory UI.
func New(
	ctx context.Context,
	roots []scan.Root,
	scanOptions scan.Options,
	options Options,
) *Model {
	model := &Model{
		baseContext:   ctx,
		roots:         roots,
		scanOptions:   scanOptions,
		options:       options,
		sortField:     inventory.SortAllocated,
		descending:    true,
		selectedStyle: lipgloss.NewStyle().Reverse(true),
		activeStyle:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		warningStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}
	model.restart()
	return model
}

// Init waits for the first scanner event.
func (m *Model) Init() tea.Cmd {
	return waitForEvent(m.events)
}

// Close cancels any active scan.
func (m *Model) Close() {
	if m.cancelScan != nil {
		m.cancelScan()
	}
}

// Update handles terminal and scanner events.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampCursor(m.visibleRows())
		return m, nil

	case scanEventMsg:
		if message.channel != m.events {
			return m, nil
		}
		if !message.ok {
			if !m.done {
				m.cancelled = true
			}
			m.done = true
			return m, nil
		}

		selectedID := m.selectedID(m.visibleRows())
		m.applyEvent(message.event)
		finished := message.event.Done
	drain:
		for processed := 1; processed < 512 && !finished; processed++ {
			select {
			case event, ok := <-message.channel:
				if !ok {
					if !m.done {
						m.cancelled = true
					}
					m.done = true
					finished = true
					break drain
				}
				m.applyEvent(event)
				finished = event.Done
			default:
				break drain
			}
		}
		m.restoreSelection(m.visibleRows(), selectedID)
		if finished {
			return m, nil
		}
		return m, waitForEvent(m.events)

	case tea.KeyMsg:
		return m.handleKey(message)
	}
	return m, nil
}

// View renders the current inventory.
func (m *Model) View() string {
	rows := m.visibleRows()
	m.clampCursor(rows)

	width := m.width
	if width <= 0 {
		width = 100
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	lines := make([]string, 0, height)
	lines = append(lines, m.renderStatus(width))
	lines = append(lines, m.renderSortAndFilter(width))
	lines = append(lines, m.renderColumns(width))

	detailLines := m.renderDetails(width, rows)
	footerLines := 2
	available := max(1, height-len(lines)-len(detailLines)-footerLines)
	m.adjustOffset(len(rows), available)

	end := min(len(rows), m.offset+available)
	for index := m.offset; index < end; index++ {
		line := m.renderRow(rows[index], width)
		if index == m.cursor && !m.options.NoColor {
			line = m.selectedStyle.Render(line)
		} else if index == m.cursor {
			line = "> " + truncate(line, max(0, width-2))
		}
		lines = append(lines, line)
	}
	for len(lines) < 3+available {
		lines = append(lines, "")
	}

	lines = append(lines, detailLines...)
	switch {
	case m.selectSort:
		lines = append(lines, m.renderSortPicker(width))
	case m.filtering:
		lines = append(lines, truncate("filter: "+m.filter+"_", width))
	default:
		lines = append(lines, truncate("arrows/jk move  pgup/pgdn page  enter expand  s choose sort  O reverse", width))
	}
	lines = append(lines, truncate("o cycle sort  / filter  c columns  d details  r rescan  q/esc quit", width))
	return strings.Join(lines, "\n")
}

func (m *Model) handleKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selectSort {
		switch message.String() {
		case "q", "ctrl+c":
			m.Close()
			return m, tea.Quit
		case "esc":
			m.selectSort = false
		case "up", "k":
			m.sortChoice = previousSortField(m.sortChoice)
		case "down", "j":
			m.sortChoice = nextSortField(m.sortChoice)
		case "enter":
			m.applySortChoice(m.sortChoice)
			m.selectSort = false
		case "a":
			m.applySortChoice(inventory.SortAllocated)
			m.selectSort = false
		case "i":
			m.applySortChoice(inventory.SortInodes)
			m.selectSort = false
		case "f":
			m.applySortChoice(inventory.SortFiles)
			m.selectSort = false
		case "p":
			m.applySortChoice(inventory.SortApparent)
			m.selectSort = false
		case "n":
			m.applySortChoice(inventory.SortName)
			m.selectSort = false
		}
		return m, nil
	}

	if m.filtering {
		switch message.String() {
		case "ctrl+c":
			m.Close()
			return m, tea.Quit
		case "esc":
			m.filtering = false
		case "enter":
			m.filtering = false
		case "backspace":
			m.filter = trimLastRune(m.filter)
			m.cursor = 0
			m.offset = 0
		case "ctrl+u":
			m.filter = ""
			m.cursor = 0
			m.offset = 0
		default:
			if message.Type == tea.KeyRunes {
				m.filter += string(message.Runes)
				m.cursor = 0
				m.offset = 0
			}
		}
		return m, nil
	}

	rows := m.visibleRows()
	switch message.String() {
	case "q", "esc", "ctrl+c":
		m.Close()
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor+1 < len(rows) {
			m.cursor++
		}
	case "pgup":
		m.cursor = max(0, m.cursor-m.pageStep(rows))
	case "pgdown":
		m.cursor = min(max(0, len(rows)-1), m.cursor+m.pageStep(rows))
	case "right", "l", "enter":
		if selected := m.selectedNode(rows); selected != nil {
			m.expanded[selected.id] = true
		}
	case "left", "h":
		if selected := m.selectedNode(rows); selected != nil {
			if m.expanded[selected.id] {
				m.expanded[selected.id] = false
			} else {
				m.selectParent(rows, selected.parentID)
			}
		}
	case "o":
		m.applySortChoice(nextSortField(m.sortField))
	case "O":
		selectedID := m.selectedID(rows)
		m.descending = !m.descending
		m.restoreSelection(m.visibleRows(), selectedID)
	case "s":
		m.sortChoice = m.sortField
		m.selectSort = true
	case "/":
		m.filtering = true
	case "c":
		m.compact = !m.compact
	case "d":
		m.details = !m.details
	case "r":
		m.restart()
		return m, waitForEvent(m.events)
	}
	m.clampCursor(m.visibleRows())
	return m, nil
}

func (m *Model) restart() {
	if m.cancelScan != nil {
		m.cancelScan()
	}
	m.scanContext, m.cancelScan = context.WithCancel(m.baseContext)
	m.events = scan.Start(m.scanContext, m.roots, m.scanOptions)
	m.nodes = make(map[string]*treeNode)
	m.rootIDs = nil
	m.expanded = make(map[string]bool)
	m.progress = scan.Progress{}
	m.summary = inventory.Metrics{}
	m.done = false
	m.cancelled = false
	m.selectSort = false
	m.cursor = 0
	m.offset = 0
}

func (m *Model) applyEvent(event scan.Event) {
	m.progress = event.Progress
	if event.Node != nil {
		update := event.Node
		node, exists := m.nodes[update.ID]
		if !exists {
			node = &treeNode{
				id:        update.ID,
				parentID:  update.ParentID,
				path:      update.Path,
				name:      update.Name,
				lowerName: strings.ToLower(update.Name),
			}
			m.nodes[update.ID] = node

			if update.Root {
				m.rootIDs = append(m.rootIDs, update.ID)
				m.expanded[update.ID] = true
			} else if parent := m.nodes[update.ParentID]; parent != nil {
				parent.children = append(parent.children, node)
			}
		}
		node.state = update.State
		node.metrics = update.Metrics
		node.errorSamples = append(node.errorSamples[:0], update.ErrorSamples...)
	}
	if event.Done {
		m.done = true
		m.cancelled = event.Cancelled
		m.summary = event.Summary
	}
}

func (m *Model) visibleRows() []row {
	initialCapacity := min(len(m.nodes), 256)
	if m.filter != "" {
		rows := make([]row, 0, initialCapacity)
		query := strings.ToLower(m.filter)
		for _, root := range m.sortedNodes(m.rootNodes()) {
			m.appendFilteredRows(&rows, root, 0, query)
		}
		return rows
	}

	rows := make([]row, 0, initialCapacity)
	var appendNode func(*treeNode, int)
	appendNode = func(node *treeNode, depth int) {
		rows = append(rows, row{id: node.id, depth: depth})
		// A childless directory can never contribute rows, so skip the
		// expansion lookup for the leaves that dominate a large tree.
		if len(node.children) == 0 || !m.expanded[node.id] {
			return
		}
		for _, child := range m.sortedNodes(node.children) {
			appendNode(child, depth+1)
		}
	}
	for _, root := range m.sortedNodes(m.rootNodes()) {
		appendNode(root, 0)
	}
	return rows
}

func (m *Model) appendFilteredRows(
	rows *[]row,
	node *treeNode,
	depth int,
	query string,
) bool {
	start := len(*rows)
	*rows = append(*rows, row{id: node.id, depth: depth})
	selfMatches := strings.Contains(node.lowerName, query) ||
		strings.Contains(strings.ToLower(node.path), query)
	childMatched := false
	for _, child := range m.sortedNodes(node.children) {
		if m.appendFilteredRows(rows, child, depth+1, query) {
			childMatched = true
		}
	}
	if selfMatches || childMatched {
		return true
	}
	*rows = (*rows)[:start]
	return false
}

// rootNodes resolves the root identifiers once per traversal. Roots are few, so
// the map lookups here are not on the hot path that children are.
func (m *Model) rootNodes() []*treeNode {
	roots := make([]*treeNode, 0, len(m.rootIDs))
	for _, rootID := range m.rootIDs {
		if node := m.nodes[rootID]; node != nil {
			roots = append(roots, node)
		}
	}
	return roots
}

// sortedNodes orders one sibling group. Children are held as pointers so the
// comparator dereferences rather than resolving path strings through m.nodes:
// hashing two long absolute paths per comparison made map lookups and string
// hashing several percent of the whole program's CPU time on a large tree.
func (m *Model) sortedNodes(nodes []*treeNode) []*treeNode {
	// Deep trees are full of single-child directories, and ordering one of
	// those is a copy and a sort call for no reordering. The result is only
	// read, so handing back the caller's slice is safe.
	if len(nodes) < 2 {
		return nodes
	}
	sorted := append([]*treeNode(nil), nodes...)
	slices.SortStableFunc(sorted, func(left *treeNode, right *treeNode) int {
		comparison := m.sortField.Compare(left.metrics, right.metrics)
		if comparison == 0 {
			comparison = strings.Compare(left.lowerName, right.lowerName)
		}
		if m.descending {
			return -comparison
		}
		return comparison
	})
	return sorted
}

func (m *Model) renderStatus(width int) string {
	state := "scanning"
	if m.done {
		switch {
		case m.cancelled:
			state = "cancelled"
		case m.summary.Errors > 0:
			state = "complete with errors"
		default:
			state = "complete"
		}
	}
	line := fmt.Sprintf(
		"dirstat | %s | dirs %s/%s | skipped %s | active %s | queued %s | errors %s",
		state,
		format.Count(m.progress.Scanned),
		format.Count(m.progress.Discovered),
		format.Count(m.progress.Skipped),
		format.Count(m.progress.Active),
		format.Count(m.progress.Queued),
		format.Count(m.progress.Errors),
	)
	if !m.options.NoColor {
		if m.progress.Errors > 0 {
			return m.warningStyle.Render(truncate(line, width))
		}
		if !m.done {
			return m.activeStyle.Render(truncate(line, width))
		}
	}
	return truncate(line, width)
}

func (m *Model) renderSortAndFilter(width int) string {
	direction := "desc"
	if !m.descending {
		direction = "asc"
	}
	line := fmt.Sprintf("sort: %s %s", m.sortField, direction)
	if m.filter != "" {
		line += fmt.Sprintf(" | filter: %q", m.filter)
	}
	return truncate(line, width)
}

func (m *Model) renderSortPicker(width int) string {
	options := []struct {
		key   string
		field inventory.SortField
	}{
		{key: "a", field: inventory.SortAllocated},
		{key: "i", field: inventory.SortInodes},
		{key: "f", field: inventory.SortFiles},
		{key: "p", field: inventory.SortApparent},
		{key: "n", field: inventory.SortName},
	}
	parts := make([]string, 0, len(options))
	for _, option := range options {
		label := option.key + ":" + option.field.String()
		if option.field == m.sortChoice {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return truncate("sort by: "+strings.Join(parts, "  ")+"  arrows+enter or letter  esc cancel", width)
}

func (m *Model) renderColumns(width int) string {
	if m.compact {
		nameWidth := max(8, width-44)
		return fmt.Sprintf(
			"%s %-11s %9s %9s %10s",
			padCells("DIRECTORY", nameWidth),
			"STATUS",
			"FILES",
			"INODES",
			"ALLOC",
		)
	}
	nameWidth := max(8, width-55)
	return fmt.Sprintf(
		"%s %-11s %9s %9s %10s %10s",
		padCells("DIRECTORY", nameWidth),
		"STATUS",
		"FILES",
		"INODES",
		"ALLOC",
		"APPARENT",
	)
}

func (m *Model) renderRow(item row, width int) string {
	node := m.nodes[item.id]
	if node == nil {
		return ""
	}

	marker := "   "
	if len(node.children) > 0 {
		if m.expanded[node.id] || m.filter != "" {
			marker = "[-]"
		} else {
			marker = "[+]"
		}
	} else if node.state == scan.StateScanning || node.state == scan.StateQueued {
		marker = "[~]"
	} else if node.state == scan.StateSkipped {
		marker = "[m]"
	} else if node.state == scan.StateAlias {
		marker = "[a]"
	}
	name := strings.Repeat("  ", item.depth) + marker + " " + format.Display(node.name)
	if node.state == scan.StatePartial {
		name += " !"
	}
	status := ""
	if node.state == scan.StateScanning || node.state == scan.StateQueued {
		status = "updating..."
	}

	if m.compact {
		nameWidth := max(8, width-44)
		return fmt.Sprintf(
			"%s %-11s %9s %9s %10s",
			padCells(name, nameWidth),
			status,
			format.Count(node.metrics.Files),
			format.Count(node.metrics.Inodes),
			format.Bytes(node.metrics.Allocated),
		)
	}
	nameWidth := max(8, width-55)
	return fmt.Sprintf(
		"%s %-11s %9s %9s %10s %10s",
		padCells(name, nameWidth),
		status,
		format.Count(node.metrics.Files),
		format.Count(node.metrics.Inodes),
		format.Bytes(node.metrics.Allocated),
		format.Bytes(node.metrics.Apparent),
	)
}

func (m *Model) renderDetails(width int, rows []row) []string {
	if !m.details {
		return nil
	}
	node := m.selectedNode(rows)
	if node == nil {
		return []string{"details: no directory selected"}
	}

	lines := []string{
		truncate(fmt.Sprintf(
			"path: %s | state: %s | dirs: %d | files: %d | inodes: %d",
			format.Display(node.path),
			node.state,
			node.metrics.Directories,
			node.metrics.Files,
			node.metrics.Inodes,
		), width),
		truncate(fmt.Sprintf(
			"allocated: %d bytes | apparent: %d bytes | rolled errors: %d",
			node.metrics.Allocated,
			node.metrics.Apparent,
			node.metrics.Errors,
		), width),
	}
	sampleLabel := "error"
	if node.state == scan.StateAlias {
		sampleLabel = "note"
	}
	for _, sample := range node.errorSamples {
		lines = append(lines, truncate(sampleLabel+": "+format.Display(sample), width))
	}
	return lines
}

func (m *Model) selectedNode(rows []row) *treeNode {
	if m.cursor < 0 || m.cursor >= len(rows) {
		return nil
	}
	return m.nodes[rows[m.cursor].id]
}

func (m *Model) selectedID(rows []row) string {
	if m.cursor < 0 || m.cursor >= len(rows) {
		return ""
	}
	return rows[m.cursor].id
}

func (m *Model) restoreSelection(rows []row, selectedID string) {
	if selectedID != "" {
		for index, item := range rows {
			if item.id == selectedID {
				m.cursor = index
				m.clampCursor(rows)
				return
			}
		}
	}
	m.clampCursor(rows)
}

func (m *Model) applySortChoice(field inventory.SortField) {
	rows := m.visibleRows()
	selectedID := m.selectedID(rows)
	m.sortField = field
	m.restoreSelection(m.visibleRows(), selectedID)
}

func (m *Model) selectParent(rows []row, parentID string) {
	for index, item := range rows {
		if item.id == parentID {
			m.cursor = index
			return
		}
	}
}

func (m *Model) clampCursor(rows []row) {
	if len(rows) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	m.cursor = min(max(0, m.cursor), len(rows)-1)
}

func (m *Model) adjustOffset(rowCount int, available int) {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+available {
		m.offset = m.cursor - available + 1
	}
	maxOffset := max(0, rowCount-available)
	m.offset = min(max(0, m.offset), maxOffset)
}

func (m *Model) pageStep(rows []row) int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	width := m.width
	if width <= 0 {
		width = 100
	}
	available := max(1, height-3-len(m.renderDetails(width, rows))-2)
	return max(1, available-1)
}

func nextSortField(field inventory.SortField) inventory.SortField {
	return (field + 1) % (inventory.SortName + 1)
}

func previousSortField(field inventory.SortField) inventory.SortField {
	if field == inventory.SortAllocated {
		return inventory.SortName
	}
	return field - 1
}

func waitForEvent(channel <-chan scan.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-channel
		return scanEventMsg{event: event, channel: channel, ok: ok}
	}
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	// A tail needs room of its own, so below its width fall back to a bare cut,
	// matching what a narrow terminal used to get.
	if width <= 3 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width, "...")
}

// padCells fits value into exactly width terminal cells. Truncation stops on a
// grapheme cluster boundary, so a two-cell cluster meeting a single remaining
// cell is dropped rather than half-printed: a partial cell cannot be drawn, and
// overflowing by one cell would shift every column on the row. The resulting
// one-cell gap is filled here, so short and overlong names align identically.
func padCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	fitted := truncate(value, width)
	return fitted + strings.Repeat(" ", max(0, width-ansi.StringWidth(fitted)))
}

func trimLastRune(value string) string {
	if value == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(value)
	return value[:len(value)-size]
}
