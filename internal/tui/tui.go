// Package tui implements the terminal UI: an fzf-style type-to-filter list with
// playback controls and output-device selection.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jrsmile/goid3db/internal/art"
	"github.com/jrsmile/goid3db/internal/audio"
	"github.com/jrsmile/goid3db/internal/model"
	"github.com/jrsmile/goid3db/internal/search"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("63"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	metaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	statusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	playingStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
)

// Model is the Bubble Tea model.
type Model struct {
	matcher *search.Matcher
	engine  *audio.Engine
	devices []audio.Device
	devIdx  int

	input  textinput.Model
	hits   []search.Hit
	cursor int
	offset int
	limit  int

	player     *audio.Player
	nowPlaying string

	// Album-art panel state.
	showArt  bool
	artPath  string   // track path the current art belongs to
	artLines []string // pre-rendered ANSI half-block lines

	width, height int
	status        string
	statusErr     bool
}

type searchResultMsg struct {
	query string
	hits  []search.Hit
}

type artLoadedMsg struct {
	path  string
	lines []string
}

type trackFinishedMsg struct{}

type tickMsg time.Time

// New builds the TUI model.
func New(matcher *search.Matcher, engine *audio.Engine, devices []audio.Device) Model {
	ti := textinput.New()
	ti.Placeholder = "fuzzy text · or filters like year:1994 genre:rock artist:\"pink floyd\"…"
	ti.Prompt = "› "
	ti.Focus()
	ti.CharLimit = 256

	// Pre-select the default device if present.
	devIdx := 0
	for i, d := range devices {
		if d.IsDefault {
			devIdx = i
			break
		}
	}

	return Model{
		matcher: matcher,
		engine:  engine,
		devices: devices,
		devIdx:  devIdx,
		input:   ti,
		limit:   200,
		showArt: true,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.doSearch(), tickEvery())
}

func tickEvery() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) doSearch() tea.Cmd {
	q := m.input.Value()
	matcher := m.matcher
	limit := m.limit
	return func() tea.Msg {
		return searchResultMsg{query: q, hits: matcher.Search(q, limit)}
	}
}

// selectedTrack returns the track under the cursor, or nil.
func (m Model) selectedTrack() *model.Track {
	if m.cursor < 0 || m.cursor >= len(m.hits) {
		return nil
	}
	return m.hits[m.cursor].Track
}

// artDims returns the album-art panel size in terminal cells, or (0,0) if the
// window is too narrow to show art.
func (m Model) artDims() (cols, rows int) {
	if !m.showArt || m.width < 70 {
		return 0, 0
	}
	cols = m.width / 4
	if cols > 28 {
		cols = 28
	}
	rows = m.visibleRows()
	return cols, rows
}

// loadArtCmd extracts and renders album art for the current selection. It is a
// no-op (returns nil) when art is already loaded for that path or disabled.
func (m Model) loadArtCmd() tea.Cmd {
	t := m.selectedTrack()
	if t == nil {
		return nil
	}
	cols, rows := m.artDims()
	if cols == 0 {
		return nil
	}
	if t.Path == m.artPath {
		return nil // already loaded
	}
	path := t.Path
	return func() tea.Msg {
		img, ok := art.Extract(path)
		if !ok {
			return artLoadedMsg{path: path, lines: nil}
		}
		return artLoadedMsg{path: path, lines: art.Render(img, cols, rows)}
	}
}

func waitForFinish(p *audio.Player) tea.Cmd {
	return func() tea.Msg {
		<-p.Done()
		return trackFinishedMsg{}
	}
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.artPath = "" // re-render art at the new panel size
		return m, m.loadArtCmd()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.stopPlayer()
			return m, tea.Quit
		case "up", "ctrl+k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.clampOffset()
			return m, m.loadArtCmd()
		case "down", "ctrl+j":
			if m.cursor < len(m.hits)-1 {
				m.cursor++
			}
			m.clampOffset()
			return m, m.loadArtCmd()
		case "pgup", "ctrl+b":
			m.cursor -= m.visibleRows()
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.clampOffset()
			return m, m.loadArtCmd()
		case "pgdown", "ctrl+f":
			m.cursor += m.visibleRows()
			if m.cursor > len(m.hits)-1 {
				m.cursor = max(0, len(m.hits)-1)
			}
			m.clampOffset()
			return m, m.loadArtCmd()
		case "enter":
			return m, m.playSelected()
		case "ctrl+p":
			if m.player != nil {
				m.player.TogglePause()
			}
			return m, nil
		case "ctrl+s":
			m.stopPlayer()
			m.nowPlaying = ""
			return m, nil
		case "ctrl+a":
			m.showArt = !m.showArt
			m.artPath = "" // force reload when re-enabled
			if m.showArt {
				m.setStatus("album art on", false)
				return m, m.loadArtCmd()
			}
			m.artLines = nil
			m.setStatus("album art off", false)
			return m, nil
		case "tab":
			if len(m.devices) > 0 {
				m.devIdx = (m.devIdx + 1) % len(m.devices)
				m.setStatus("output → "+m.devices[m.devIdx].Name, false)
			}
			return m, nil
		}

		// Forward everything else to the search input, then re-search.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, tea.Batch(cmd, m.doSearch())

	case searchResultMsg:
		// Ignore stale results from earlier keystrokes.
		if msg.query == m.input.Value() {
			m.hits = msg.hits
			if m.cursor >= len(m.hits) {
				m.cursor = max(0, len(m.hits)-1)
			}
			m.clampOffset()
			return m, m.loadArtCmd()
		}
		return m, nil

	case artLoadedMsg:
		// Apply only if it still matches the current selection.
		if t := m.selectedTrack(); t != nil && t.Path == msg.path {
			m.artPath = msg.path
			m.artLines = msg.lines
		}
		return m, nil

	case trackFinishedMsg:
		m.nowPlaying = ""
		return m, nil

	case tickMsg:
		// Periodically re-run the search so files indexed in the background
		// (by the watcher) appear without user interaction.
		return m, tea.Batch(m.doSearch(), tickEvery())
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) playSelected() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.hits) {
		return nil
	}
	track := m.hits[m.cursor].Track
	m.stopPlayer()

	var dev *audio.Device
	if len(m.devices) > 0 {
		dev = &m.devices[m.devIdx]
	}
	p, err := m.engine.Play(track.Path, dev)
	if err != nil {
		m.setStatus("play error: "+err.Error(), true)
		return nil
	}
	m.player = p
	m.nowPlaying = displayName(track)
	out := "default"
	if dev != nil {
		out = dev.Name
	}
	m.setStatus("playing on "+out, false)
	return waitForFinish(p)
}

func (m *Model) stopPlayer() {
	if m.player != nil {
		m.player.Stop()
		m.player = nil
	}
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder

	header := titleStyle.Render("goid3db") + dimStyle.Render(
		fmt.Sprintf("  %d tracks indexed", m.matcher.Len()))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")

	// Build the results list, then optionally join an album-art panel beside it.
	listBlock := m.renderList()
	if artCols, _ := m.artDims(); artCols > 0 && len(m.artLines) > 0 {
		artBlock := strings.Join(m.artLines, "\n")
		body := lipgloss.JoinHorizontal(lipgloss.Top, listBlock, "  ", artBlock)
		b.WriteString(body)
	} else {
		b.WriteString(listBlock)
	}
	b.WriteString("\n\n")

	if m.nowPlaying != "" {
		state := "▶"
		if m.player != nil && m.player.Paused() {
			state = "⏸"
		}
		b.WriteString(playingStyle.Render(state+" "+m.nowPlaying) + "\n")
	}
	if m.status != "" {
		if m.statusErr {
			b.WriteString(errStyle.Render(m.status) + "\n")
		} else {
			b.WriteString(statusStyle.Render(m.status) + "\n")
		}
	}

	out := "default"
	if len(m.devices) > 0 {
		out = m.devices[m.devIdx].Name
	}
	help := dimStyle.Render(fmt.Sprintf(
		"↑/↓ move · pgup/pgdn page · enter play · ctrl+p pause · ctrl+s stop · ctrl+a art · tab device [%s] · esc quit", out))
	hint := dimStyle.Render("filters: year:1994 · year:1990..1999 · genre:rock · artist:\"pink floyd\" + fuzzy text")
	b.WriteString(help + "\n" + hint)
	return b.String()
}

// renderList renders the visible slice of search results as a single block.
func (m Model) renderList() string {
	var b strings.Builder
	rows := m.visibleRows()
	if len(m.hits) == 0 {
		b.WriteString(dimStyle.Render("  no matches"))
		return b.String()
	}
	for i := m.offset; i < m.offset+rows && i < len(m.hits); i++ {
		t := m.hits[i].Track
		line := fmt.Sprintf("%-40s %s", truncate(displayName(t), 40),
			metaStyle.Render(truncate(meta(t), 36)))
		if i == m.cursor {
			b.WriteString(selectedStyle.Render("▶ " + line))
		} else {
			b.WriteString("  " + line)
		}
		if i < m.offset+rows-1 && i < len(m.hits)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// clampOffset keeps the scroll offset so the cursor stays within the visible window.
func (m *Model) clampOffset() {
	rows := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m Model) visibleRows() int {
	// Reserve lines for header, input, now-playing, status and help.
	r := m.height - 9
	if r < 5 {
		r = 5
	}
	if r > m.limit {
		r = m.limit
	}
	return r
}

func displayName(t *model.Track) string {
	if t.Title != "" {
		if t.Artist != "" {
			return t.Artist + " – " + t.Title
		}
		return t.Title
	}
	return t.Path
}

func meta(t *model.Track) string {
	parts := make([]string, 0, 3)
	if t.Album != "" {
		parts = append(parts, t.Album)
	}
	if t.Genre != "" {
		parts = append(parts, t.Genre)
	}
	if t.Year > 0 {
		parts = append(parts, fmt.Sprintf("%d", t.Year))
	}
	return strings.Join(parts, " · ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
