package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PR is the pull request of a lane's branch, as far as a dashboard row needs it.
type PR struct {
	Number int
	Title  string
	State  string
	Head   string
	Draft  bool
	// Checks is pass, fail, running, or empty when the PR reports none.
	Checks string
	Branch string
	Author string
	Bot    bool
	Cross  bool
}

// LaneView is a lane with everything read live: its work tree, its agent, its
// PR, which of its apps answer.
type LaneView struct {
	Lane
	Dirty     int
	Ahead     int
	Behind    int
	Agent     string
	AgentPane string
	Workspace string
	PR        *PR
	// Tree is the content id of the checkout, read only for lanes with a proof or checks to compare it to.
	Tree       string
	Up         []string
	Down       []string
	InEmulator bool
	// Touched are the apps the lane changed files of, against its base branch.
	Touched []string
	// Install is "" when the lane's apps are installed, else "installing" or "missing".
	Install string
}

type prCache struct {
	at   time.Time
	prs  map[string]*PR
	open []*PR
}

var (
	prMu    sync.Mutex
	prByDir = map[string]prCache{}
)

// prsOf reads a repo's pull requests, keyed by head branch, and keeps them for
// a while: the dashboard asks every few seconds. Open ones are read in full,
// closed and merged ones only as far back as the lanes can still care about.
func prsOf(repoPath string, maxAge time.Duration) map[string]*PR {
	prs, _ := prData(repoPath, maxAge)
	return prs
}

// openPRsOf lists a repo's open pull requests a person opened, newest first.
// A bot's (Renovate's) are left out: they are not work to pick up here.
func openPRsOf(repoPath string, maxAge time.Duration) []*PR {
	_, open := prData(repoPath, maxAge)
	return open
}

const prFields = "number,title,state,isDraft,headRefName,headRefOid,statusCheckRollup,author,isCrossRepository"

func prData(repoPath string, maxAge time.Duration) (map[string]*PR, []*PR) {
	prMu.Lock()
	defer prMu.Unlock()

	if cached, ok := prByDir[repoPath]; ok && time.Since(cached.at) < maxAge {
		return cached.prs, cached.open
	}

	type item struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Draft  bool   `json:"isDraft"`
		Branch string `json:"headRefName"`
		Head   string `json:"headRefOid"`
		Cross  bool   `json:"isCrossRepository"`
		Author struct {
			Login string `json:"login"`
			IsBot bool   `json:"is_bot"`
		} `json:"author"`
		Rollup []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	read := func(state string, limit string) ([]item, error) {
		out, err := runTimeout(repoPath, 25*time.Second, "gh", "pr", "list", "--state", state, "--limit", limit, "--json", prFields)
		if err != nil {
			return nil, err
		}
		var list []item
		return list, json.Unmarshal([]byte(out), &list)
	}

	opened, err := read("open", "100")
	if err != nil {
		if cached, ok := prByDir[repoPath]; ok {
			return cached.prs, cached.open
		}
		return map[string]*PR{}, nil
	}
	recent, _ := read("all", "40")

	prs := map[string]*PR{}
	var open []*PR
	for _, it := range append(opened, recent...) {
		// gh lists newest first, and the open ones come first: the first PR seen
		// for a branch is its current one.
		if _, seen := prs[it.Branch]; seen {
			continue
		}
		pr := &PR{Number: it.Number, Title: it.Title, State: it.State, Head: it.Head, Draft: it.Draft,
			Branch: it.Branch, Author: it.Author.Login, Bot: it.Author.IsBot, Cross: it.Cross}
		for _, check := range it.Rollup {
			switch {
			case check.Conclusion == "FAILURE" || check.Conclusion == "TIMED_OUT" || check.Conclusion == "CANCELLED" || check.State == "FAILURE" || check.State == "ERROR":
				pr.Checks = "fail"
			case check.Status != "" && check.Status != "COMPLETED" || check.State == "PENDING":
				if pr.Checks != "fail" {
					pr.Checks = "running"
				}
			default:
				if pr.Checks == "" {
					pr.Checks = "pass"
				}
			}
		}
		prs[it.Branch] = pr
		if pr.State == "OPEN" && !pr.Bot {
			open = append(open, pr)
		}
	}

	prByDir[repoPath] = prCache{at: time.Now(), prs: prs, open: open}
	return prs, open
}

func prOf(repoPath, branch string) *PR {
	return prsOf(repoPath, time.Minute)[branch]
}

// agentRank orders herdr's agent states by how much they want the person.
var agentRank = map[string]int{"blocked": 5, "done": 4, "working": 3, "idle": 2, "unknown": 1}

// views reads every lane of every registered repo. `withPRs` false skips GitHub.
func views(withPRs bool) []LaneView {
	state := loadState()
	agents := herdrAgents()
	installing := setupHolder()
	var out []LaneView

	for _, repo := range loadGlobal().Repos {
		lanes := lanesOf(repo, state)
		cfg := loadRepoConfig(repo.Path, "")
		open := herdrWorkspaceOf(repo.Path)
		var prs map[string]*PR
		if withPRs {
			prs = prsOf(repo.Path, 45*time.Second)
		}

		rows := make([]LaneView, len(lanes))
		var wg sync.WaitGroup
		for i, lane := range lanes {
			rows[i] = LaneView{Lane: lane, Workspace: open[lane.key()]}
			if !lane.Managed {
				continue
			}
			wg.Add(1)
			go func(view *LaneView) {
				defer wg.Done()
				if status, err := run(view.Path, "git", "status", "--porcelain"); err == nil && status != "" {
					view.Dirty = len(strings.Split(status, "\n"))
				}
				if counts, err := run(view.Path, "git", "rev-list", "--left-right", "--count", "HEAD...origin/"+cfg.Base); err == nil {
					fields := strings.Fields(counts)
					if len(fields) == 2 {
						view.Ahead, _ = strconv.Atoi(fields[0])
						view.Behind, _ = strconv.Atoi(fields[1])
					}
				}
				if view.State != nil && (view.State.Proof != nil || view.State.Checks != nil) {
					view.Tree = treeOf(view.Path)
				}
				if !view.IsMain {
					for _, app := range touchedApps(view.Lane, cfg) {
						view.Touched = append(view.Touched, app.Name)
					}
				}
				for _, app := range cfg.Apps {
					if app.Port == 0 || app.Dev == "" {
						continue
					}
					if listening(app.port(view.Slot)) {
						view.Up = append(view.Up, app.Name)
					} else {
						view.Down = append(view.Down, app.Name)
					}
				}
			}(&rows[i])
		}
		wg.Wait()

		for i := range rows {
			view := &rows[i]
			// An agent belongs to the lane whose path is the longest prefix of its cwd.
			for _, agent := range agents {
				if !within(agent.Cwd, view.Path) || ownedByDeeper(agent.Cwd, view.Path, lanes) {
					continue
				}
				if agentRank[agent.Status] > agentRank[view.Agent] {
					view.Agent, view.AgentPane = agent.Status, agent.PaneID
				}
			}
			if view.Branch != "" && view.Branch != cfg.Base {
				view.PR = prs[view.Branch]
			}
			view.InEmulator = state.Emulator != nil && state.Emulator.Lane == view.key()
			if len(missingSetup(view.Lane, cfg)) > 0 {
				view.Install = "missing"
				if installing == view.key() {
					view.Install = "installing"
				}
			}
		}
		out = append(out, rows...)
	}

	return out
}

func ownedByDeeper(cwd, path string, lanes []Lane) bool {
	for _, other := range lanes {
		if len(other.Path) > len(path) && within(cwd, other.Path) {
			return true
		}
	}
	return false
}

// --- kitt ls / env -----------------------------------------------------------

func cmdLs(args []string) error {
	_, opts := flags(args, "json", "all")
	rows := views(true)

	if opts["json"] != "" {
		type row struct {
			Repo, Name, Path, Branch, Agent string
			Slot, Dirty, Ahead, Behind      int
			Managed, InEmulator             bool
			PR                              *PR
			Checks                          *CheckResult
			Proof                           *ProofState
		}
		var list []row
		for _, v := range rows {
			item := row{Repo: v.Repo, Name: v.Name, Path: v.Path, Branch: v.Branch, Agent: v.Agent, Slot: v.Slot,
				Dirty: v.Dirty, Ahead: v.Ahead, Behind: v.Behind, Managed: v.Managed, InEmulator: v.InEmulator, PR: v.PR}
			if v.State != nil {
				item.Checks, item.Proof = v.State.Checks, v.State.Proof
			}
			list = append(list, item)
		}
		return json.NewEncoder(os.Stdout).Encode(list)
	}

	unmanaged := 0
	for _, v := range rows {
		if !v.Managed && opts["all"] == "" {
			unmanaged++
			continue
		}
		fmt.Println(plainRow(v))
	}
	if unmanaged > 0 {
		fmt.Printf("+ %d other worktrees (kitt ls --all; kitt adopt <name> makes one a lane)\n", unmanaged)
	}
	return nil
}

func plainRow(v LaneView) string {
	parts := []string{fmt.Sprintf("%-28s", v.Repo+"/"+v.Name)}
	if v.Agent != "" {
		parts = append(parts, fmt.Sprintf("%-8s", v.Agent))
	} else {
		parts = append(parts, fmt.Sprintf("%-8s", "-"))
	}
	git := ""
	if v.Dirty > 0 {
		git += fmt.Sprintf("±%d ", v.Dirty)
	}
	if v.Behind > 0 {
		git += fmt.Sprintf("↓%d ", v.Behind)
	}
	parts = append(parts, fmt.Sprintf("%-9s", git))
	parts = append(parts, fmt.Sprintf("%-14s", prText(v)))
	parts = append(parts, fmt.Sprintf("%-10s", proofText(v)))
	if v.InEmulator {
		parts = append(parts, "emulator")
	}
	return strings.TrimRight(strings.Join(parts, " "), " ")
}

func prText(v LaneView) string {
	if v.PR == nil {
		return "-"
	}
	mark := map[string]string{"pass": "✓", "fail": "✗", "running": "●", "": ""}[v.PR.Checks]
	switch v.PR.State {
	case "MERGED":
		return fmt.Sprintf("#%d merged", v.PR.Number)
	case "CLOSED":
		return fmt.Sprintf("#%d closed", v.PR.Number)
	}
	return strings.TrimSpace(fmt.Sprintf("#%d %s", v.PR.Number, mark))
}

func proofText(v LaneView) string {
	if v.State == nil || v.State.Proof == nil {
		return "-"
	}
	proof := v.State.Proof
	switch proof.Status {
	case "running":
		return "proof …"
	case "pass":
		if proof.Tree != v.Tree {
			return "proof stale"
		}
		return "proof ✓"
	case "fail":
		return "proof ✗"
	}
	return "-"
}

// cmdEnv prints what a lane's processes should know: its ports, by app. An
// agent reads this instead of guessing 8081.
func cmdEnv(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	if !lane.Managed {
		return fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	fmt.Printf("KITT_LANE=%s\nKITT_SLOT=%d\nKITT_ROOT=%s\n", lane.Name, lane.Slot, lane.Path)
	for _, app := range cfg.Apps {
		if app.Port > 0 {
			fmt.Printf("KITT_PORT_%s=%d\n", strings.ToUpper(strings.ReplaceAll(app.Name, "-", "_")), app.port(lane.Slot))
		}
	}
	return nil
}
