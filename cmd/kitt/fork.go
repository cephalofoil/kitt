package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A fork is a sub-lane for one slice of a lane's work, for an implementer agent
// that must be able to commit, hooks included. It is a worktree branched from
// the lane's tip with its own install, so what a pre-commit hook runs is the
// fork's own: git does not carry node_modules (or the hook shims a package
// manager's prepare script writes) into a worktree, and a link to another
// checkout's node_modules is not the fork's install. Its commits come back by a
// merge or cherry-pick into the lane's branch; `kitt rm <fork>` then removes it
// together with its branch, without --force.

// forkNames gives a fork's lane name and branch: <lane>-<slice> for both
// parts, with the lane's own branch as the prefix of the branch.
func forkNames(parent Lane, slice string) (name, branch string) {
	slice = slug(slice, 30)
	base := parent.Branch
	if base == "" {
		base = parent.Name
	}
	return parent.Name + "-" + slice, base + "-" + slice
}

func cmdFork(args []string) error {
	rest, opts := flags(args, "no-setup")
	if len(rest) != 1 || slug(rest[0], 30) == "" {
		return fail("usage: kitt fork <slice> [--lane l] [--apps web,admin] [--no-setup]")
	}
	parent, err := findLane(opts["lane"])
	if err != nil {
		return err
	}
	if parent.IsMain {
		return fail("a fork is made from a lane: the main checkout has no branch of its own to fork from")
	}
	if parent.Branch == "" {
		return fail("%s has no branch checked out: a fork needs its tip", parent.Name)
	}
	if parent.State != nil && parent.State.ForkOf != "" {
		return fail("%s is a fork itself: fork its lane %s instead", parent.Name, parent.State.ForkOf)
	}
	cfg := loadRepoConfig(parent.Main, parent.Path)
	name, branch := forkNames(parent, rest[0])
	path := filepath.Join(cfg.lanesDir(parent.Main), name)
	if exists(path) {
		return fail("%s exists already: remove that fork first (kitt rm %s) or pick another slice name", path, name)
	}
	if _, err := run(parent.Main, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return fail("the branch %s exists already: pick another slice name", branch)
	}
	if status, _ := run(parent.Path, "git", "status", "--porcelain"); strings.TrimSpace(status) != "" {
		fmt.Printf("note: %s has uncommitted changes; a fork starts from its last commit, without them\n", parent.Name)
	}
	var apps []string
	if opts["apps"] != "" {
		apps = strings.Split(opts["apps"], ",")
		for _, app := range apps {
			if cfg.app(app) == nil {
				return fail("%s has no app named %q", parent.Repo, app)
			}
		}
	} else if parent.State != nil {
		apps = parent.State.Apps
	}

	tip, err := run(parent.Path, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := runTimeout(parent.Main, 90*time.Second, "git", "worktree", "add", "-b", branch, path, tip); err != nil {
		return err
	}
	// Nothing pushed by accident to the parent's remote branch.
	_, _ = run(path, "git", "branch", "--unset-upstream")

	entry := LaneState{Repo: parent.Repo, Name: name, Path: path, Branch: branch, Created: time.Now(), Apps: apps, ForkOf: parent.Name, ForkBranch: parent.Branch}
	if parent.State != nil {
		entry.Issue, entry.Title, entry.URL, entry.Ticket = parent.State.Issue, parent.State.Title, parent.State.URL, parent.State.Ticket
	}
	if err := updateState(func(s *State) {
		entry.Slot = s.freeSlot(parent.Repo)
		s.Lanes[norm(path)] = &entry
	}); err != nil {
		return err
	}
	lane := Lane{Repo: parent.Repo, Main: parent.Main, Path: path, Name: name, Branch: branch, Head: tip, Slot: entry.Slot, Managed: true, State: &entry}
	fmt.Printf("fork %s of %s  slot %d  %s\n  %s at %s\n", name, parent.Name, lane.Slot, branch, path, short(tip))
	linkFiles(lane, cfg, os.Stdout)

	if opts["no-setup"] == "" {
		setupLane(lane, cfg)
	}
	if problem := hooksProblem(path); problem != "" {
		fmt.Printf("warning: %s\n", problem)
	}
	fmt.Printf("commit in %s with the repo's hooks on (never --no-verify), then in %s: git merge %s, and kitt rm %s\n", path, parent.Name, branch, name)
	return nil
}

// hooksProblem says why a commit in dir would skip the repo's hooks, or "".
// A hooksPath the repo sets in its config (husky: .husky/_) names a folder the
// install writes and git does not carry: without it git skips every hook, silently.
func hooksProblem(dir string) string {
	hooks, err := run(dir, "git", "config", "--get", "core.hooksPath")
	if err != nil || hooks == "" {
		return ""
	}
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(dir, filepath.FromSlash(hooks))
	}
	if exists(hooks) {
		return ""
	}
	return fmt.Sprintf("core.hooksPath points at %s, which is missing: commits here would run no hooks until the install has run (kitt setup)", hooks)
}

func short(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

// forkMerged says a fork has nothing to lose: every commit of it is in the
// branch of the lane it was forked from.
func forkMerged(lane Lane) bool {
	if lane.State == nil || lane.State.ForkOf == "" || lane.State.ForkBranch == "" {
		return false
	}
	_, err := run(lane.Path, "git", "merge-base", "--is-ancestor", "HEAD", lane.State.ForkBranch)
	return err == nil
}
