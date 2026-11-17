package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// appBackend is the herdr app: a lane is a space named after it, its folder
// the lane's worktree. The window is driven with `herdr-app do`, its
// terminals with herdr-core's `hc`. A pane here is a terminal address (t7).
type appBackend struct {
	cached *appList
	at     time.Time
}

// appList is what `herdr-app ls` prints.
type appList struct {
	Spaces []appSpace `json:"spaces"`
}

type appSpace struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Folder  string   `json:"folder"`
	Machine string   `json:"machine"`
	Active  bool     `json:"active"`
	Tabs    []appTab `json:"tabs"`
}

type appTab struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Active    bool          `json:"active"`
	Terminals []appTerminal `json:"terminals"`
}

type appTerminal struct {
	Terminal string `json:"terminal"`
	Pane     string `json:"pane"`
	Status   string `json:"status"`
	App      string `json:"app"`
	Name     string `json:"name"`
}

// appMade is what `herdr-app do` answers: what the command made.
type appMade struct {
	Space    string `json:"space"`
	Tab      string `json:"tab"`
	Terminal string `json:"terminal"`
	Pane     string `json:"pane"`
}

func parseAppList(data []byte) (*appList, error) {
	var list appList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fail("herdr-app ls: unreadable answer")
	}
	// Spaces on other machines are not where a lane's worktree is.
	local := list.Spaces[:0]
	for _, space := range list.Spaces {
		if space.Machine == "" {
			local = append(local, space)
		}
	}
	list.Spaces = local
	return &list, nil
}

// spaceOf finds the lane's space: the one whose folder is the lane's, else
// one named after the lane whose folder lies in it.
func (l *appList) spaceOf(lane Lane) *appSpace {
	for i := range l.Spaces {
		if norm(l.Spaces[i].Folder) == lane.key() {
			return &l.Spaces[i]
		}
	}
	for i := range l.Spaces {
		if l.Spaces[i].Name == lane.Name && within(l.Spaces[i].Folder, lane.Path) {
			return &l.Spaces[i]
		}
	}
	return nil
}

func (l *appList) space(id string) *appSpace {
	for i := range l.Spaces {
		if l.Spaces[i].ID == id {
			return &l.Spaces[i]
		}
	}
	return nil
}

func (a *appBackend) list() (*appList, error) {
	if a.cached != nil && time.Since(a.at) < 2*time.Second {
		return a.cached, nil
	}
	out, err := runTimeout("", 10*time.Second, "herdr-app", "ls")
	if err != nil {
		return nil, err
	}
	list, err := parseAppList([]byte(out))
	if err != nil {
		return nil, err
	}
	a.cached, a.at = list, time.Now()
	return list, nil
}

// do runs an app command with its arguments as JSON.
func (a *appBackend) do(command string, args map[string]any) (appMade, error) {
	a.cached = nil
	var made appMade
	data, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "herdr-app", "do", command, string(data))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	made, problem := parseAppMade(out.Bytes())
	if problem != "" {
		return made, fail("herdr-app %s: %s", command, problem)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return made, fail("herdr-app %s: %s", command, firstLine(msg))
	}
	return made, nil
}

// parseAppMade reads an answer of `herdr-app do`: the ids of what it made, or
// its error. An id may come as a string or as an object with an id.
func parseAppMade(data []byte) (appMade, string) {
	var made appMade
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return made, ""
	}
	id := func(key string) string {
		var text string
		if json.Unmarshal(fields[key], &text) == nil {
			return text
		}
		var object struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(fields[key], &object)
		return object.ID
	}
	made = appMade{Space: id("space"), Tab: id("tab"), Terminal: id("terminal"), Pane: id("pane")}
	var problem string
	_ = json.Unmarshal(fields["error"], &problem)
	return made, problem
}

func (*appBackend) kind() string { return herdrApp }

// terminalCwds reads where each terminal is now from herdr-core.
func terminalCwds() map[string]string {
	cwds := map[string]string{}
	out, err := runTimeout("", 10*time.Second, "hc", "ls", "--json")
	if err != nil {
		return cwds
	}
	var result struct {
		Terminals []struct {
			Address string `json:"address"`
			Cwd     string `json:"cwd"`
		} `json:"terminals"`
	}
	_ = json.Unmarshal([]byte(out), &result)
	for _, t := range result.Terminals {
		cwds[t.Address] = t.Cwd
	}
	return cwds
}

// appAgents are the terminals of a list that report a status, where they are.
func appAgents(list *appList, cwds map[string]string) []herdrAgent {
	var agents []herdrAgent
	for _, space := range list.Spaces {
		for _, tab := range space.Tabs {
			for _, t := range tab.Terminals {
				if t.Status == "" {
					continue
				}
				cwd := cwds[t.Terminal]
				if cwd == "" {
					cwd = space.Folder
				}
				agents = append(agents, herdrAgent{Agent: t.App, Status: t.Status, Cwd: cwd, PaneID: t.Terminal,
					TabID: tab.ID, WorkspaceID: space.ID, Title: t.Name})
			}
		}
	}
	return agents
}

func (a *appBackend) agents() []herdrAgent {
	list, err := a.list()
	if err != nil {
		return nil
	}
	return appAgents(list, terminalCwds())
}

func (a *appBackend) workspaces(string) map[string]string {
	open := map[string]string{}
	list, err := a.list()
	if err != nil {
		return open
	}
	for _, space := range list.Spaces {
		if space.Folder != "" {
			open[norm(space.Folder)] = space.ID
		}
	}
	return open
}

func (a *appBackend) open(lane Lane, focus bool) (string, error) {
	list, err := a.list()
	if err != nil {
		return "", err
	}
	if space := list.spaceOf(lane); space != nil {
		if focus {
			_, err := a.do("space.select", map[string]any{"id": space.ID})
			return space.ID, err
		}
		return space.ID, nil
	}
	// The main checkout is the repo's own space: unnamed, so Herdr calls it after the repo.
	if lane.IsMain || norm(lane.Path) == norm(lane.Main) {
		made, err := a.do("space.new", map[string]any{"folder": lane.Path, "focus": focus})
		return made.Space, err
	}
	if err := a.repoSpace(list, lane.Main); err != nil {
		return "", err
	}
	made, err := a.do("space.new", map[string]any{"name": lane.Name, "folder": lane.Path, "focus": focus})
	return made.Space, err
}

// repoSpace makes sure the repo's main checkout has a space. Herdr's sidebar
// nests a worktree's space under its main checkout's, the way the TUI lists a
// repo's lanes under it, but only while that space exists.
func (a *appBackend) repoSpace(list *appList, main string) error {
	if main == "" {
		return nil
	}
	for _, space := range list.Spaces {
		if space.Machine == "" && norm(space.Folder) == norm(main) {
			return nil
		}
	}
	_, err := a.do("space.new", map[string]any{"folder": main, "focus": false})
	return err
}

func (a *appBackend) newWorktree(repoPath, branch, base, path, label string, focus bool) (string, error) {
	if _, err := runTimeout(repoPath, 60*time.Second, "git", "worktree", "add", "-b", branch, path, base); err != nil {
		return "", err
	}
	if list, err := a.list(); err == nil {
		if err := a.repoSpace(list, repoPath); err != nil {
			return "", err
		}
	}
	made, err := a.do("space.new", map[string]any{"name": label, "folder": path, "focus": focus})
	return made.Space, err
}

func (a *appBackend) close(space string) error {
	_, err := a.do("space.close", map[string]any{"id": space, "confirmed": true})
	return err
}

func (a *appBackend) focusAgent(terminal string) error {
	_, err := a.do("agent.focus", map[string]any{"terminal": terminal})
	return err
}

// startAgent starts the agent in a tab of its own: the space's shell is the
// person's, nothing is typed into it.
func (a *appBackend) startAgent(space string, lane Lane, kind, _ string, tab string) (string, error) {
	if tab == "" {
		tab = kind
	}
	made, err := a.do("tab.new", map[string]any{"name": tab, "space": space, "folder": lane.Path, "argv": []string{kind}, "focus": false})
	if err != nil {
		return "", err
	}
	if made.Terminal == "" {
		return "", fail("herdr-app tab.new: no terminal in its answer")
	}
	// A prompt is only taken once the agent is up and says so.
	for waited := 0; waited < 60 && !agentReady(made.Terminal); waited++ {
		time.Sleep(time.Second)
	}
	return made.Terminal, nil
}

// agentReady is whether a terminal's program reports an agent status yet.
func agentReady(terminal string) bool {
	out, err := runTimeout("", 5*time.Second, "hc", "get", terminal, "--json")
	if err != nil {
		return false
	}
	var t struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	return json.Unmarshal([]byte(out), &t) == nil && t.Status.State != ""
}

func (*appBackend) prompt(terminal, text string) error {
	// "--": a prompt that starts with a dash is text, not an option.
	_, err := runTimeout("", 90*time.Second, "hc", "send", "--", terminal, text)
	return err
}

// serve starts a dev server through `kitt run-env`: the app takes no
// environment for a terminal, so kitt hands it over in a file it deletes on
// reading, and no secret is typed into a shell.
func (a *appBackend) serve(space, dir string, env map[string]string, title, command string) error {
	file, err := writeRunEnv(runEnv{Dir: dir, Command: command, Title: title, Env: env})
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	argv := []string{exe, "run-env", file}

	list, err := a.list()
	if err != nil {
		_ = os.Remove(file)
		return err
	}
	last := ""
	if s := list.space(space); s != nil {
		for _, tab := range s.Tabs {
			if tab.Name == devTab && len(tab.Terminals) > 0 {
				last = tab.Terminals[len(tab.Terminals)-1].Pane
			}
		}
	}
	if last == "" {
		_, err = a.do("tab.new", map[string]any{"name": devTab, "space": space, "folder": dir, "argv": argv, "focus": false})
	} else {
		_, err = a.do("pane.split", map[string]any{"direction": "right", "pane": last, "argv": argv})
	}
	if err != nil {
		_ = os.Remove(file)
	}
	return err
}

func (a *appBackend) stopDev(space string) int {
	list, err := a.list()
	if err != nil {
		return 0
	}
	s := list.space(space)
	if s == nil {
		return 0
	}
	closed := 0
	for _, tab := range s.Tabs {
		if tab.Name == devTab || strings.HasPrefix(tab.Name, "dev:") {
			if _, err := a.do("tab.close", map[string]any{"id": tab.ID, "confirmed": true}); err == nil {
				closed++
			}
		}
	}
	return closed
}

func (a *appBackend) dash(exe string) (string, error) {
	home, _ := os.UserHomeDir()
	if list, err := a.list(); err == nil {
		for _, s := range list.Spaces {
			if s.Name == "kitt" && norm(s.Folder) == norm(home) {
				return s.ID, nil
			}
		}
	}
	made, err := a.do("space.new", map[string]any{"name": "kitt", "folder": home, "argv": []string{exe, "--herdr=app", "dash"}, "focus": false})
	return made.Space, err
}

// runEnv is a dev server handed to `kitt run-env`.
type runEnv struct {
	Dir     string            `json:"dir"`
	Command string            `json:"command"`
	Title   string            `json:"title"`
	Env     map[string]string `json:"env"`
}

func writeRunEnv(r runEnv) (string, error) {
	dir := filepath.Join(configDir(), "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "serve-*.json")
	if err != nil {
		return "", err
	}
	defer file.Close()
	_ = file.Chmod(0o600)
	return file.Name(), json.NewEncoder(file).Encode(r)
}

// cmdRunEnv runs a dev server kitt handed over in a file: in its directory,
// with its environment, under its title. The file is gone once read.
func cmdRunEnv(args []string) error {
	if len(args) != 1 {
		return fail("usage: kitt run-env <file> (kitt starts dev servers in the herdr app this way)")
	}
	var r runEnv
	err := readJSON(args[0], &r)
	_ = os.Remove(args[0])
	if err != nil {
		return err
	}
	// The terminal's title says what runs in it.
	fmt.Printf("\x1b]0;%s\x07", r.Title)
	cmd := shell(r.Dir, r.Command)
	cmd.Env = os.Environ()
	for key, value := range r.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		return err
	}
	return nil
}
