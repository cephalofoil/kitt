package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Herdr is the terminal workspace manager the lanes live in. Every call goes
// through its CLI, which answers {"result": ...} as JSON.

type herdrAgent struct {
	Agent       string `json:"agent"`
	Status      string `json:"agent_status"`
	Cwd         string `json:"cwd"`
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"terminal_title_stripped"`
}

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

var herdrFound *bool

func hasHerdr() bool {
	if herdrFound == nil {
		_, err := exec.LookPath("herdr")
		ok := err == nil && os.Getenv("KITT_NO_HERDR") == ""
		if ok {
			_, err = runTimeout("", 3*time.Second, "herdr", "workspace", "list")
			ok = err == nil
		}
		herdrFound = &ok
	}
	return *herdrFound
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

func herdrAgents() []herdrAgent {
	var result struct {
		Agents []herdrAgent `json:"agents"`
	}
	if !hasHerdr() || herdr(&result, "agent", "list") != nil {
		return nil
	}
	return result.Agents
}

// herdrWorkspaceOf maps each worktree path of a repo to the workspace it is open in.
func herdrWorkspaceOf(repoPath string) map[string]string {
	var result struct {
		Worktrees []herdrWorktree `json:"worktrees"`
	}
	open := map[string]string{}
	if !hasHerdr() || herdr(&result, "worktree", "list", "--cwd", repoPath) != nil {
		return open
	}
	for _, wt := range result.Worktrees {
		if wt.WorkspaceID != "" {
			open[norm(wt.Path)] = wt.WorkspaceID
		}
	}
	return open
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

// herdrOpen makes sure the lane's worktree is open as a workspace and returns its id.
func herdrOpen(lane Lane, focus bool) (string, error) {
	if ws := herdrWorkspaceOf(lane.Main)[norm(lane.Path)]; ws != "" {
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
