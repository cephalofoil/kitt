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

// prRow is an open pull request that has no lane yet.
type prRow struct {
	Repo string
	PR   *PR
}

type dash struct {
	rows    []LaneView
	prs     []prRow
	cursor  int
	showAll bool
	width   int
	height  int
	loaded  bool

	note   string
	noteAt time.Time
	busy   string

	// mode is "", "new", "remove" or "force".
	mode  string
	input string
	// held is the lane a refused removal named, and why it was refused.
	held   string
	reason string
}

type (
	tickMsg time.Time
	rowsMsg struct {
		lanes []LaneView
		prs   []prRow
	}
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

func load() tea.Msg {
	lanes := views(true)
	hasLane := map[string]bool{}
	for _, lane := range lanes {
		if lane.Managed {
			hasLane[lane.Repo+"\x00"+lane.Branch] = true
		}
	}
	var prs []prRow
	for _, repo := range loadGlobal().Repos {
		for _, pr := range openPRsOf(repo.Path, 45*time.Second) {
			if !hasLane[repo.Name+"\x00"+pr.Branch] {
				prs = append(prs, prRow{Repo: repo.Name, PR: pr})
			}
		}
	}
	return rowsMsg{lanes: lanes, prs: prs}
}

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
		d.rows, d.prs, d.loaded = msg.lanes, msg.prs, true
		if count := len(d.visible()) + len(d.prs); d.cursor >= count {
			d.cursor = max(0, count-1)
		}
		return d, nil

	case doneMsg:
		d.busy, d.note, d.noteAt = "", string(msg), time.Now()
		// A removal kitt refused holds unsaved work: ask a second time, differently.
		if text := string(msg); d.held != "" && strings.Contains(text, "--force") {
			d.mode, d.reason = "force", strings.TrimPrefix(strings.Split(text, " (pass --force")[0], "kitt: ")
		} else {
			d.held = ""
		}
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
		d.cursor = min(len(rows)+len(d.prs)-1, d.cursor+1)
	case "r":
		return d, load
	case "t":
		d.showAll = !d.showAll
		d.cursor = 0
	case "n":
		d.mode, d.input = "new", ""
	}
	// An open pull request: enter checks it out as a lane, o shows it on GitHub.
	if at := d.cursor - len(rows); row == nil && at >= 0 && at < len(d.prs) {
		pr := d.prs[at]
		number := fmt.Sprint(pr.PR.Number)
		switch msg.String() {
		case "enter":
			return start("checking out PR #"+number, "pr", number, "--repo", pr.Repo)
		case "o":
			repoPath := ""
			for _, repo := range loadGlobal().Repos {
				if repo.Name == pr.Repo {
					repoPath = repo.Path
				}
			}
			d.busy = "browser → PR #" + number
			return d, func() tea.Msg {
				if _, err := run(repoPath, "gh", "pr", "view", number, "--web"); err != nil {
					return doneMsg(err.Error())
				}
				return doneMsg("opened PR #" + number)
			}
		case "e", "g", "i", "u", "d", "c", "p", "a", "x":
			return say("PR #" + number + " has no lane yet: enter checks it out")
		}
		return d, nil
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
		// Its own process: an install it has to run first must not write over this screen.
		return start("opening "+row.Name, "focus", target)
	case "e":
		// Its own process, like enter: in the single mode it takes the stack, which may stop another lane.
		return start("emulator → "+row.Name, "emu", target)
	case "g":
		if !row.Managed {
			return say(row.Name + " is not a lane yet: press a to adopt it")
		}
		return start("agent for "+row.Name, "agent", target)
	case "o":
		lane := row.Lane
		d.busy = "browser → " + row.Name
		return d, func() tea.Msg {
			message, err := openWeb(lane, nil)
			if err != nil {
				return doneMsg(err.Error())
			}
			return doneMsg(message)
		}
	case "i":
		if !row.Managed {
			return say(row.Name + " is not a lane yet: press a to adopt it")
		}
		return start("installing "+row.Name, "setup", target)
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
	} else if at := d.cursor - len(rows); at >= 0 && at < len(d.prs) {
		repo = d.prs[at].Repo
	} else if repos := loadGlobal().Repos; len(repos) > 0 {
		repo = repos[0].Name
	}

	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		d.mode = ""
		return d, nil

	case tea.KeyEnter:
		mode, input := d.mode, strings.TrimSpace(d.input)
		d.mode = ""
		if mode == "remove" || mode == "force" || input == "" {
			d.held = ""
			return d, nil
		}
		// "412 web admin": an issue, then the apps the lane is about.
		args := []string{"new", input, "--repo", repo}
		if words := strings.Fields(input); len(words) > 1 && isNumber(words[0]) {
			args = []string{"new", words[0], "--repo", repo, "--apps", strings.Join(words[1:], ",")}
		}
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
		if d.mode == "remove" && d.cursor < len(rows) {
			row := rows[d.cursor]
			d.mode = ""
			if msg.String() == "y" {
				d.busy, d.held = "removing "+row.Name, row.Repo+"/"+row.Name
				return d, self("rm", d.held)
			}
			return d, nil
		}
		if d.mode == "force" {
			target := d.held
			d.mode, d.held = "", ""
			if msg.String() == "D" {
				d.busy = "deleting " + target
				return d, self("rm", target, "--force")
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
	needs, emulator, hidden, held := 0, "", 0, ""
	for _, row := range d.rows {
		if row.Lease != nil && row.Lease.Kind == "hard" {
			held = row.Name + " (" + row.Lease.Reason + ")"
		}
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
	// The header: the logo, and beside it what matters at a glance, one fact a line.
	mark := logo(width)
	lanes := len(d.rows) - hidden
	first := bold.Render(fmt.Sprintf("%d lanes", lanes))
	if lanes == 1 {
		first = bold.Render("1 lane")
	}
	if len(d.prs) > 0 {
		first += dim.Render(fmt.Sprintf(" · %d open PRs without one", len(d.prs)))
	}
	if mark != nil && !logoNamesItself(width) {
		first = accent.Render("kitt") + dim.Render(" · ") + first
	}
	second := dim.Render("nobody is waiting for you")
	if needs > 0 {
		second = red.Render(fmt.Sprintf("%d waiting for you", needs))
	}
	third := dim.Render("emulator free")
	if emulator != "" {
		third = dim.Render("emulator → ") + cyan.Render(emulator)
	}
	if held != "" {
		third += dim.Render(" · stack held by ") + yellow.Render(held)
	}
	info := []string{first, second, third}

	b.WriteString("\n")
	if mark == nil {
		b.WriteString("  " + accent.Render("kitt") + "  " + strings.Join(info, dim.Render(" · ")) + "\n")
	}
	for i, line := range mark {
		// The facts sit against the logo's lower lines.
		if at := i - (len(mark) - len(info)); at >= 0 {
			line += "   " + info[at]
		}
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n")

	const nameW, appsW, agentW, gitW, prW, proofW = 26, 14, 10, 10, 13, 12
	titleW := min(46, max(12, width-(4+nameW+appsW+agentW+gitW+prW+proofW+8+7)))

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
		case row.State != nil && row.State.Title != "" && row.State.Issue > 0:
			title = fmt.Sprintf("#%d %s", row.State.Issue, row.State.Title)
		case row.State != nil && row.State.Title != "":
			title = row.State.Title
		case row.PR != nil:
			title = row.PR.Title
		}

		b.WriteString(pointer + agentDot(row.Agent) + " " + name + " " + dim.Render(cut(title, titleW)) + " " + cyan.Render(cut(strings.Join(row.Touched, "·"), appsW)) + " " +
			agentCell(row.Agent, agentW) + gitCell(row, gitW) + prCell(row, prW) + proofCell(row, proofW) + marks(row) + "\n")
	}
	if hidden > 0 && !d.showAll {
		b.WriteString("\n  " + dim.Render(fmt.Sprintf("+ %d other worktrees · t shows them", hidden)) + "\n")
	}

	// Open pull requests without a lane: fetched and checked out only when asked.
	if len(d.prs) > 0 {
		b.WriteString("\n  " + dim.Render("open pull requests · enter checks one out as a lane") + "\n")
		for i, pr := range d.prs {
			pointer := "  "
			label := cut(fmt.Sprintf("#%d", pr.PR.Number), 6)
			if len(rows)+i == d.cursor {
				pointer = accent.Render("▸ ")
				label = bold.Render(label)
			}
			state := prCell(LaneView{PR: pr.PR}, prW)
			draft := ""
			if pr.PR.Draft {
				draft = dim.Render(" draft")
			}
			b.WriteString(pointer + dim.Render("◦ ") + label + cut(pr.PR.Title, min(70, max(20, width-60))) + " " +
				dim.Render(cut(pr.PR.Branch, 34)) + " " + state + draft + "\n")
		}
	}

	b.WriteString("\n")
	switch {
	case d.mode == "new" || d.mode == "new-agent":
		what := "new lane"
		if d.mode == "new-agent" {
			what = "new lane with an agent"
		}
		b.WriteString("  " + accent.Render(what) + dim.Render(" · issue number (plus apps, like \"412 web\") or a name: ") + d.input + "▏\n")
	case d.mode == "remove" && d.cursor < len(rows):
		b.WriteString("  " + red.Render("remove "+rows[d.cursor].Name+"?") + dim.Render(" y removes the worktree · any other key keeps it") + "\n")
	case d.mode == "force":
		b.WriteString("  " + red.Render(d.reason) + dim.Render(" · ") + red.Render("D") + dim.Render(" deletes it and everything unsaved in it · any other key keeps it") + "\n")
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
		{"enter", "open lane"}, {"e", "emulator"}, {"o", "browser"}, {"g", "agent"}, {"i", "install"}, {"u", "up"}, {"d", "down"}, {"c", "check"},
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
	// A lane that cannot run yet says so before anything else.
	switch row.Install {
	case "installing":
		return yellow.Render(cut("installing…", width))
	case "missing":
		return red.Render(cut("no install·i", width))
	}
	text := proofText(row)
	if text == "-" {
		text = ""
		if row.State != nil && row.State.Checks != nil {
			text = "checks ✓"
			if len(row.State.Checks.Failed) > 0 {
				text = "checks ✗"
			}
			if row.State.Checks.Tree != row.Tree {
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
	if row.Lease != nil && row.Lease.Kind == "hard" {
		out += yellow.Render("⚿")
	}
	return out
}
