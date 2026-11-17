package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tuiBackend is the herdr TUI. Every call goes through its CLI, which answers
// {"result": ...} as JSON.
type tuiBackend struct{}

type herdrPane struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Agent       string `json:"agent"`
	Cwd         string `json:"cwd"`
}

type herdrTab struct {
	TabID string `json:"tab_id"`
	Label string `json:"label"`
}

type herdrWorktree struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	WorkspaceID string `json:"open_workspace_id"`
}

func herdr(into any, args ...string) error {
	return herdrTimeout(into, 20*time.Second, args...)
}

func herdrTimeout(into any, limit time.Duration, args ...string) error {
	out, err := runTimeout("", limit, "herdr", args...)
	if err != nil {
		return err
	}
	if into == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return fail("herdr %s: unreadable answer", firstWord(args))
	}
	return json.Unmarshal(envelope.Result, into)
}

func herdrPanes(workspace string) []herdrPane {
	var result struct {
		Panes []herdrPane `json:"panes"`
	}
	if herdr(&result, "pane", "list", "--workspace", workspace) != nil {
		return nil
	}
	return result.Panes
}

func herdrTabs(workspace string) []herdrTab {
	var result struct {
		Tabs []herdrTab `json:"tabs"`
	}
	if herdr(&result, "tab", "list", "--workspace", workspace) != nil {
		return nil
	}
	return result.Tabs
}

func (tuiBackend) kind() string { return herdrTUI }

func (tuiBackend) agents() []herdrAgent {
	var result struct {
		Agents []herdrAgent `json:"agents"`
	}
	if herdr(&result, "agent", "list") != nil {
		return nil
	}
	return result.Agents
}

func (tuiBackend) workspaces(repoPath string) map[string]string {
	var result struct {
		Worktrees []herdrWorktree `json:"worktrees"`
	}
	open := map[string]string{}
	if herdr(&result, "worktree", "list", "--cwd", repoPath) != nil {
		return open
	}
	for _, wt := range result.Worktrees {
		if wt.WorkspaceID != "" {
			open[norm(wt.Path)] = wt.WorkspaceID
		}
	}
	return open
}

func (t tuiBackend) open(lane Lane, focus bool) (string, error) {
	if ws := t.workspaces(lane.Main)[norm(lane.Path)]; ws != "" {
		if focus {
			return ws, herdr(nil, "workspace", "focus", ws)
		}
		return ws, nil
	}

	var result struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	args := []string{"worktree", "open", "--cwd", lane.Main, "--path", lane.Path, "--label", lane.Name}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	if err := herdr(&result, args...); err != nil {
		return "", err
	}
	return result.Workspace.ID, nil
}

func (tuiBackend) newWorktree(repoPath, branch, base, path, label string, focus bool) (string, error) {
	var result struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	args := []string{"worktree", "create", "--cwd", repoPath, "--branch", branch, "--base", base,
		"--path", filepath.ToSlash(path), "--label", label}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	if err := herdrTimeout(&result, 60*time.Second, args...); err != nil {
		return "", err
	}
	return result.Workspace.ID, nil
}

func (tuiBackend) close(workspace string) error {
	return herdr(nil, "workspace", "close", workspace)
}

func (tuiBackend) focusAgent(pane string) error {
	return herdr(nil, "agent", "focus", pane)
}

func (tuiBackend) startAgent(workspace string, lane Lane, kind, name, tab string, env map[string]string) (string, error) {
	pane := ""
	// The shell pane has its environment already: an agent that needs more gets a tab made with it.
	if tab == "" && len(env) > 0 {
		tab = kind
	}
	if tab == "" {
		devTabs := map[string]bool{}
		for _, t := range herdrTabs(workspace) {
			if strings.HasPrefix(t.Label, "dev:") {
				devTabs[t.TabID] = true
			}
		}
		for _, candidate := range herdrPanes(workspace) {
			if !devTabs[candidate.TabID] {
				pane = candidate.PaneID
				break
			}
		}
		if pane == "" {
			return "", fail("workspace %s has no shell pane to start an agent in", workspace)
		}
	} else {
		var created struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		}
		args := []string{"tab", "create", "--workspace", workspace, "--cwd", lane.Path, "--label", tab, "--no-focus"}
		for _, key := range sortedKeys(env) {
			args = append(args, "--env", key+"="+env[key])
		}
		if err := herdr(&created, args...); err != nil {
			return "", err
		}
		pane = created.RootPane.ID
	}
	if err := herdrTimeout(nil, 90*time.Second, "agent", "start", name, "--kind", kind, "--pane", pane, "--timeout", "60000"); err != nil {
		return "", err
	}
	return pane, nil
}

func (tuiBackend) prompt(pane, text string) error {
	return herdr(nil, "agent", "prompt", pane, text)
}

func (tuiBackend) serve(workspace, dir string, env map[string]string, title, command string) (string, error) {
	pane, err := devPane(workspace, dir, env)
	if err != nil {
		return "", err
	}
	_ = herdr(nil, "pane", "rename", pane, title)
	return pane, herdr(nil, "pane", "run", pane, command)
}

func (tuiBackend) closePane(pane string) error {
	return herdr(nil, "pane", "close", pane)
}

// devPane makes the pane an app's dev server runs in: the dev tab's first pane,
// or a split to the right of its last one. The environment is the pane's own,
// set as it is created, so no secret is typed into a shell.
func devPane(workspace, dir string, env map[string]string) (string, error) {
	var envArgs []string
	for _, key := range sortedKeys(env) {
		envArgs = append(envArgs, "--env", key+"="+env[key])
	}

	tab := ""
	for _, candidate := range herdrTabs(workspace) {
		if candidate.Label == devTab {
			tab = candidate.TabID
		}
	}
	if tab == "" {
		var created struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		}
		args := append([]string{"tab", "create", "--workspace", workspace, "--cwd", dir, "--label", devTab, "--no-focus"}, envArgs...)
		if err := herdr(&created, args...); err != nil {
			return "", err
		}
		return created.RootPane.ID, nil
	}

	last := ""
	for _, pane := range herdrPanes(workspace) {
		if pane.TabID == tab {
			last = pane.PaneID
		}
	}
	if last == "" {
		return "", fail("the dev tab of workspace %s has no pane", workspace)
	}
	var split struct {
		Pane struct {
			ID string `json:"pane_id"`
		} `json:"pane"`
	}
	args := append([]string{"pane", "split", last, "--direction", "right", "--cwd", dir, "--no-focus"}, envArgs...)
	if err := herdr(&split, args...); err != nil {
		return "", err
	}
	return split.Pane.ID, nil
}

func (tuiBackend) stopDev(workspace string) int {
	closed := 0
	for _, tab := range herdrTabs(workspace) {
		if tab.Label == devTab || strings.HasPrefix(tab.Label, "dev:") {
			if herdr(nil, "tab", "close", tab.TabID) == nil {
				closed++
			}
		}
	}
	return closed
}

func (tuiBackend) dash(exe string) (string, error) {
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
		return "", err
	}
	// The pane inherits herdr's environment, which may predate kitt on PATH: name the binary.
	if err := herdr(nil, "pane", "run", created.RootPane.ID, "& '"+exe+"' dash"); err != nil {
		return "", err
	}
	return created.Workspace.ID, nil
}
