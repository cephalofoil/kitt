package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Herdr is where the lanes live: the herdr TUI (workspaces) or the herdr app
// (spaces). A lane is one workspace or space, its agents and dev servers are
// panes in it. Both are driven through their CLIs, behind one interface.

type herdrAgent struct {
	Agent       string `json:"agent"`
	Status      string `json:"agent_status"`
	Cwd         string `json:"cwd"`
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"terminal_title_stripped"`
}

// herdrBackend is what kitt asks of herdr. A workspace is the TUI's workspace
// or the app's space; a pane is the TUI's pane or the app's terminal address.
type herdrBackend interface {
	kind() string
	agents() []herdrAgent
	// workspaces maps each open worktree path of a repo (normalized) to its workspace.
	workspaces(repoPath string) map[string]string
	open(lane Lane, focus bool) (string, error)
	// newWorktree makes the lane's worktree and opens it.
	newWorktree(repoPath, branch, base, path, label string, focus bool) (string, error)
	close(workspace string) error
	focusAgent(pane string) error
	// startAgent starts an agent of kind in the lane: in its shell pane when
	// tab is "", else in a new tab of that name. It returns the agent's pane.
	// env is what the agent needs beyond the lane's shell (agentEnv).
	startAgent(workspace string, lane Lane, kind, name, tab string, env map[string]string) (string, error)
	prompt(pane, text string) error
	// serve runs a dev server in the dev tab, titled title, and returns its pane.
	serve(workspace, dir string, env map[string]string, title, command string) (string, error)
	// closePane ends what runs in one pane (a dev server kitt started) and closes it.
	closePane(pane string) error
	// stopDev closes the dev tabs and says how many.
	stopDev(workspace string) int
	// dash opens the dashboard as a workspace of its own and returns it.
	dash(exe string) (string, error)
}

const (
	herdrApp  = "app"
	herdrTUI  = "tui"
	herdrAuto = "auto"
)

// herdrFlag is --herdr=…, given before the command.
var herdrFlag string

var (
	herdrPicked  bool
	herdrUsing   herdrBackend
	herdrWhyText string
)

func validHerdrMode(mode string) bool {
	return mode == herdrApp || mode == herdrTUI || mode == herdrAuto
}

// herdrMode is the backend asked for and who asked: the flag, KITT_HERDR,
// `kitt herdr use`, else auto.
func herdrMode() (string, string) {
	if herdrFlag != "" {
		return herdrFlag, "--herdr"
	}
	if mode := strings.ToLower(os.Getenv("KITT_HERDR")); validHerdrMode(mode) {
		return mode, "KITT_HERDR"
	}
	if mode := loadGlobal().Herdr; validHerdrMode(mode) {
		return mode, "kitt herdr use"
	}
	return herdrAuto, "default"
}

// pickHerdr decides which herdr a mode means: "app", "tui" or "" for none, and why.
func pickHerdr(mode, by string, getenv func(string) string, appUp, tuiUp func() bool) (string, string) {
	if getenv("KITT_NO_HERDR") != "" {
		return "", "KITT_NO_HERDR is set"
	}
	switch mode {
	case herdrApp:
		if appUp() {
			return herdrApp, "asked for by " + by
		}
		return "", "the herdr app is asked for by " + by + ", but it does not answer"
	case herdrTUI:
		if tuiUp() {
			return herdrTUI, "asked for by " + by
		}
		return "", "the herdr TUI is asked for by " + by + ", but its server does not answer"
	}
	switch {
	case getenv("HC_TERMINAL_ID") != "" && appUp():
		return herdrApp, "auto: inside a herdr app terminal"
	case getenv("HERDR_PANE_ID") != "" && tuiUp():
		return herdrTUI, "auto: inside a herdr pane"
	case appUp():
		return herdrApp, "auto: the herdr app answers"
	case tuiUp():
		return herdrTUI, "auto: the herdr server answers"
	}
	return "", "auto: neither the herdr app nor the herdr server answers"
}

func herdrNow() herdrBackend {
	if !herdrPicked {
		herdrPicked = true
		mode, by := herdrMode()
		kind, why := pickHerdr(mode, by, os.Getenv, appAnswers, tuiAnswers)
		herdrWhyText = why
		switch kind {
		case herdrApp:
			herdrUsing = &appBackend{}
		case herdrTUI:
			herdrUsing = tuiBackend{}
		}
	}
	return herdrUsing
}

func hasHerdr() bool { return herdrNow() != nil }

// herdrName says which herdr a message is about.
func herdrName() string {
	if b := herdrNow(); b != nil && b.kind() == herdrApp {
		return "the herdr app"
	}
	return "herdr"
}

func appAnswers() bool {
	if _, err := exec.LookPath("herdr-app"); err != nil {
		return false
	}
	_, err := runTimeout("", 3*time.Second, "herdr-app", "status")
	return err == nil
}

func tuiAnswers() bool {
	if _, err := exec.LookPath("herdr"); err != nil {
		return false
	}
	_, err := runTimeout("", 3*time.Second, "herdr", "workspace", "list")
	return err == nil
}

func herdrAgents() []herdrAgent {
	if !hasHerdr() {
		return nil
	}
	return herdrNow().agents()
}

// herdrWorkspaceOf maps each worktree path of a repo to the workspace it is open in.
func herdrWorkspaceOf(repoPath string) map[string]string {
	if !hasHerdr() {
		return map[string]string{}
	}
	return herdrNow().workspaces(repoPath)
}

// herdrOpen makes sure the lane's worktree is open as a workspace and returns its id.
func herdrOpen(lane Lane, focus bool) (string, error) {
	if !hasHerdr() {
		return "", fail("herdr is not running")
	}
	return herdrNow().open(lane, focus)
}

// agentName fits a lane name into what herdr accepts for an agent: [a-z][a-z0-9_-]{0,31}.
func agentName(lane string) string {
	name := slug(lane, 32)
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "l-" + name
	}
	if len(name) > 32 {
		name = strings.TrimRight(name[:32], "-")
	}
	return name
}

// cmdHerdr says which herdr kitt uses, or with `use`, sets it.
func cmdHerdr(args []string) error {
	rest, _ := flags(args)
	if len(rest) > 0 {
		if rest[0] != "use" || len(rest) < 2 || !validHerdrMode(rest[1]) {
			return fail("usage: kitt herdr [use app|tui|auto]")
		}
		g := loadGlobal()
		g.Herdr = rest[1]
		if rest[1] == herdrAuto {
			g.Herdr = ""
		}
		if err := saveGlobal(g); err != nil {
			return err
		}
		fmt.Printf("kitt uses %s from now on (KITT_HERDR and --herdr still win)\n", rest[1])
		return nil
	}
	mode, by := herdrMode()
	using := "none"
	if b := herdrNow(); b != nil {
		using = b.kind()
	}
	fmt.Printf("herdr   %s  (%s)\n", using, herdrWhyText)
	fmt.Printf("asked   %s  (by %s)\n", mode, by)
	fmt.Printf("app     %s\n", upText(appAnswers()))
	fmt.Printf("tui     %s\n", upText(tuiAnswers()))
	return nil
}

func upText(ok bool) string {
	if ok {
		return "answers"
	}
	return "does not answer"
}

// herdrArgs takes --herdr=app|tui|auto off the front of the arguments.
func herdrArgs(args []string) ([]string, error) {
	for len(args) > 0 && (args[0] == "--herdr" || strings.HasPrefix(args[0], "--herdr=")) {
		value, ok := strings.CutPrefix(args[0], "--herdr=")
		args = args[1:]
		if !ok {
			if len(args) == 0 {
				return nil, fail("--herdr takes app, tui or auto")
			}
			value, args = args[0], args[1:]
		}
		if !validHerdrMode(value) {
			return nil, fail("--herdr takes app, tui or auto, not %q", value)
		}
		herdrFlag = value
		// What kitt starts (the dashboard's commands) uses the same herdr.
		_ = os.Setenv("KITT_HERDR", value)
	}
	return args, nil
}
