package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The dashboard: one row per lane, the few facts that decide what to do next,
// and one key to go into a lane (its chat in herdr, its app in the emulator).

var (
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold    = lipgloss.NewStyle().Bold(true)
	red     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	green   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	cyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	magenta = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
)

type dash struct {
	rows    []LaneView
	cursor  int
	showAll bool
	width   int
	height  int
	loaded  bool

	note   string
	noteAt time.Time
	busy   string

	// mode is "", "new", "new-agent" or "remove".
	mode  string
	input string
}

type (
	tickMsg time.Time
	rowsMsg []LaneView
	doneMsg string
)

func cmdDash(args []string) error {
	_, opts := flags(args, "workspace")
	if opts["workspace"] != "" {
		return dashWorkspace()
	}
	if len(loadGlobal().Repos) == 0 {
		return fail("no repos registered: run `kitt repo add` inside a repo first")
	}
	_, err := tea.NewProgram(dash{}, tea.WithAltScreen()).Run()
	return err
}

// dashWorkspace opens the dashboard as its own herdr workspace.
func dashWorkspace() error {
	if !hasHerdr() {
		return fail("herdr is not running")
	}
	var created struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane struct {
			ID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	home, _ := os.UserHomeDir()
	if err := herdr(&created, "workspace", "create", "--label", "kitt", "--cwd", home, "--no-focus"); err != nil {
		return err
	}
	// The pane inherits herdr's environment, which may predate kitt on PATH: name the binary.
	exe, _ := os.Executable()
	if err := herdr(nil, "pane", "run", created.RootPane.ID, "& '"+exe+"' dash"); err != nil {
		return err
	}
	fmt.Printf("dashboard is workspace %s (\"kitt\")\n", created.Workspace.ID)
	return nil
}

func load() tea.Msg { return rowsMsg(views(true)) }

func tick() tea.Cmd {
	return tea.Tick(4*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (d dash) Init() tea.Cmd { return tea.Batch(load, tick()) }

// self runs another kitt command as its own process: its output would tear the
// screen if it printed here. The last line it says becomes the note.
func self(args ...string) tea.Cmd {
	return func() tea.Msg {
		exe, _ := os.Executable()
		out, err := exec.Command(exe, args...).CombinedOutput()
		lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")), "\n")
		last := lines[len(lines)-1]
		if err != nil && last == "" {
			last = err.Error()
		}
		return doneMsg(last)
	}
}

func (d dash) visible() []LaneView {
	var rows []LaneView
	for _, row := range d.rows {
		if row.Managed || d.showAll {
			rows = append(rows, row)
		}
	}
	return rows
}

func (d dash) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case tickMsg:
		return d, tea.Batch(load, tick())

	case rowsMsg:
		d.rows, d.loaded = msg, true
		if rows := d.visible(); d.cursor >= len(rows) {
			d.cursor = max(0, len(rows)-1)
		}
		return d, nil

	case doneMsg:
		d.busy, d.note, d.noteAt = "", string(msg), time.Now()
		return d, load

	case tea.KeyMsg:
		if d.mode != "" {
			return d.typed(msg)
		}
		return d.pressed(msg)
	}
	return d, nil
}

func (d dash) pressed(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := d.visible()
	var row *LaneView
	if d.cursor < len(rows) {
		row = &rows[d.cursor]
	}
	say := func(text string) (tea.Model, tea.Cmd) {
		d.note, d.noteAt = text, time.Now()
		return d, nil
	}
	start := func(what string, args ...string) (tea.Model, tea.Cmd) {
		d.busy = what
		return d, self(args...)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return d, tea.Quit
	case "up", "k":
		d.cursor = max(0, d.cursor-1)
	case "down", "j":
		d.cursor = min(len(rows)-1, d.cursor+1)
	case "r":
		return d, load
	case "t":
		d.showAll = !d.showAll
		d.cursor = 0
	case "n":
		d.mode, d.input = "new", ""
	}
	if row == nil {
		return d, nil
	}
	target := row.Repo + "/" + row.Name

	switch msg.String() {
	case "enter":
		if !row.Managed {
			return say(row.Name + " is not a lane yet: press a to adopt it")
		}
		lane, pane := row.Lane, row.AgentPane
		d.busy = "opening " + row.Name
		return d, func() tea.Msg { return doneMsg(strings.Join(focus(lane, pane), " · ")) }
	case "e":
		lane := row.Lane
		d.busy = "emulator → " + row.Name
		return d, func() tea.Msg {
			message, err := pointEmulator(lane)
			if err != nil {
				return doneMsg(err.Error())
			}
			return doneMsg(message)
		}
	case "g":
		if !row.Managed {
			return say(row.Name + " is not a lane yet: press a to adopt it")
		}
		return start("agent for "+row.Name, "agent", target)
	case "u":
		return start("starting dev servers of "+row.Name, "up", target)
	case "d":
		return start("stopping dev servers of "+row.Name, "down", target)
	case "c":
		return start("checking "+row.Name, "check", target)
	case "a":
		if row.Managed {
			return say(row.Name + " is a lane already")
		}
		return start("adopting "+row.Name, "adopt", row.Path)
	case "p":
		if row.State == nil || row.State.Proof == nil {
			return say(row.Name + " has no proof yet")
		}
		return start("opening proof", "proof", "open", target)
	case "x":
		if row.IsMain {
			return say("the main checkout is not removed")
		}
		d.mode, d.input = "remove", ""
	}
	return d, nil
}

func (d dash) typed(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := d.visible()
	repo := ""
	if d.cursor < len(rows) {
		repo = rows[d.cursor].Repo
	}

	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		d.mode = ""
		return d, nil

	case tea.KeyEnter:
		mode, input := d.mode, strings.TrimSpace(d.input)
		d.mode = ""
		if mode == "remove" || input == "" {
			return d, nil
		}
		args := []string{"new", input, "--repo", repo}
		if mode == "new-agent" {
			args = append(args, "--agent")
		}
		d.busy = "creating lane " + input
		return d, self(args...)

	case tea.KeyBackspace:
		if r := []rune(d.input); len(r) > 0 {
			d.input = string(r[:len(r)-1])
		}
		return d, nil

	case tea.KeyRunes, tea.KeySpace:
		if d.mode == "remove" {
			row := rows[d.cursor]
			d.mode = ""
			if msg.String() == "y" {
				d.busy = "removing " + row.Name
				return d, self("rm", row.Repo+"/"+row.Name)
			}
			return d, nil
		}
		d.input += msg.String()
	}
	return d, nil
}

func cut(s string, width int) string {
	r := []rune(s)
	if width <= 0 {
		return ""
	}
	if len(r) > width {
		return string(r[:max(0, width-1)]) + "…"
	}
	return s + strings.Repeat(" ", width-len(r))
}

func (d dash) View() string {
	if !d.loaded {
		return "\n  " + dim.Render("reading lanes…")
	}
	width := d.width
	if width <= 0 {
		width = 100
	}
	rows := d.visible()

	var b strings.Builder
	needs, emulator, hidden := 0, "", 0
	for _, row := range d.rows {
		if !row.Managed {
			hidden++
		}
		if row.Agent == "blocked" || row.Agent == "done" {
			needs++
		}
		if row.InEmulator {
			emulator = row.Name
		}
	}
	head := accent.Render("kitt") + dim.Render(fmt.Sprintf("  %d lanes", len(d.rows)-hidden))
	if needs > 0 {
		head += dim.Render(" · ") + red.Render(fmt.Sprintf("%d waiting for you", needs))
	}
	if emulator != "" {
		head += dim.Render(" · emulator → ") + cyan.Render(emulator)
	}
	b.WriteString("\n  " + head + "\n\n")

	const nameW, agentW, gitW, prW, proofW = 26, 10, 10, 13, 12
	titleW := min(46, max(12, width-(4+nameW+agentW+gitW+prW+proofW+8+6)))

	repo := ""
	for i, row := range rows {
		if row.Repo != repo {
			repo = row.Repo
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("  " + dim.Render(repo) + "\n")
		}

		pointer := "  "
		name := cut(row.Name, nameW)
		if i == d.cursor {
			pointer = accent.Render("▸ ")
			name = bold.Render(name)
		}
		if !row.Managed {
			b.WriteString(pointer + dim.Render("· "+cut(row.Name, nameW)+" "+cut(row.Branch, titleW)+" not a lane") + "\n")
			continue
		}

		title := row.Branch
		switch {
		case row.State != nil && row.State.Title != "":
			title = fmt.Sprintf("#%d %s", row.State.Issue, row.State.Title)
		case row.PR != nil:
			title = row.PR.Title
		}

		b.WriteString(pointer + agentDot(row.Agent) + " " + name + " " + dim.Render(cut(title, titleW)) + " " +
			agentCell(row.Agent, agentW) + gitCell(row, gitW) + prCell(row, prW) + proofCell(row, proofW) + marks(row) + "\n")
	}
	if hidden > 0 && !d.showAll {
		b.WriteString("\n  " + dim.Render(fmt.Sprintf("+ %d other worktrees · t shows them", hidden)) + "\n")
	}

	b.WriteString("\n")
	switch {
	case d.mode == "new" || d.mode == "new-agent":
		what := "new lane"
		if d.mode == "new-agent" {
			what = "new lane with an agent"
		}
		b.WriteString("  " + accent.Render(what) + dim.Render(" · an issue number starts an agent on it, a name only makes the lane: ") + d.input + "▏\n")
	case d.mode == "remove" && d.cursor < len(rows):
		b.WriteString("  " + red.Render("remove "+rows[d.cursor].Name+"?") + dim.Render(" y removes the worktree · any other key keeps it") + "\n")
	case d.busy != "":
		b.WriteString("  " + yellow.Render("… "+d.busy) + "\n")
	case d.note != "" && time.Since(d.noteAt) < 20*time.Second:
		b.WriteString("  " + cut(d.note, width-4) + "\n")
	default:
		b.WriteString("\n")
	}
	b.WriteString("\n" + legend(width))

	return b.String()
}

// legend lists every key, wrapped to the pane: a narrow split must not cut one off.
func legend(width int) string {
	keys := [][2]string{
		{"enter", "open lane"}, {"e", "emulator"}, {"g", "agent"}, {"u", "up"}, {"d", "down"}, {"c", "check"},
		{"p", "proof"}, {"n", "new"}, {"a", "adopt"}, {"x", "remove"}, {"t", "all worktrees"}, {"q", "quit"},
	}
	var b strings.Builder
	line := 2
	b.WriteString("  ")
	for i, key := range keys {
		item := len([]rune(key[0])) + 1 + len([]rune(key[1]))
		if i > 0 {
			if line+3+item > width-2 {
				b.WriteString("\n  ")
				line = 2
			} else {
				b.WriteString(dim.Render(" · "))
				line += 3
			}
		}
		b.WriteString(accent.Render(key[0]) + " " + dim.Render(key[1]))
		line += item
	}
	b.WriteString("\n  " + green.Render("▲") + dim.Render(" dev servers up · ") + cyan.Render("▣") + dim.Render(" in the emulator") + "\n")
	return b.String()
}

func agentDot(status string) string {
	switch status {
	case "blocked":
		return red.Render("●")
	case "done":
		return green.Render("●")
	case "working":
		return yellow.Render("●")
	case "idle":
		return dim.Render("○")
	}
	return dim.Render("·")
}

func agentCell(status string, width int) string {
	switch status {
	case "blocked":
		return red.Render(cut("needs you", width))
	case "done":
		return green.Render(cut("finished", width))
	case "working":
		return yellow.Render(cut("working", width))
	case "idle":
		return dim.Render(cut("idle", width))
	}
	return dim.Render(cut("–", width))
}

func gitCell(row LaneView, width int) string {
	var parts []string
	if row.Dirty > 0 {
		parts = append(parts, fmt.Sprintf("±%d", row.Dirty))
	}
	if row.Behind > 0 && !row.IsMain {
		parts = append(parts, fmt.Sprintf("↓%d", row.Behind))
	}
	text := cut(strings.Join(parts, " "), width)
	if row.Dirty > 0 {
		return cyan.Render(text)
	}
	return yellow.Render(text)
}

func prCell(row LaneView, width int) string {
	if row.PR == nil {
		return cut("", width)
	}
	text := cut(prText(row), width)
	switch {
	case row.PR.State == "MERGED":
		return magenta.Render(text)
	case row.PR.State == "CLOSED":
		return dim.Render(text)
	case row.PR.Checks == "fail":
		return red.Render(text)
	case row.PR.Checks == "running":
		return yellow.Render(text)
	}
	return green.Render(text)
}

func proofCell(row LaneView, width int) string {
	text := proofText(row)
	if text == "-" {
		text = ""
		if row.State != nil && row.State.Checks != nil {
			text = "checks ✓"
			if len(row.State.Checks.Failed) > 0 {
				text = "checks ✗"
			}
			if row.State.Checks.Commit != row.Head {
				text += "?"
			}
		}
		if strings.Contains(text, "✗") {
			return red.Render(cut(text, width))
		}
		return dim.Render(cut(text, width))
	}
	switch {
	case strings.Contains(text, "✗"):
		return red.Render(cut(text, width))
	case strings.Contains(text, "✓"):
		return green.Render(cut(text, width))
	}
	return yellow.Render(cut(text, width))
}

func marks(row LaneView) string {
	out := ""
	switch {
	case len(row.Up) > 0 && len(row.Down) == 0:
		out += green.Render("▲ ")
	case len(row.Up) > 0:
		out += yellow.Render("▲ ")
	default:
		out += "  "
	}
	if row.InEmulator {
		out += cyan.Render("▣")
	}
	return out
}
