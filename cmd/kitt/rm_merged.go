package main

import (
	"fmt"
	"strings"
	"time"
)

// mergedFacts is what `kitt rm --merged` knows about one lane, read before
// anything is decided: the decision itself touches nothing.
type mergedFacts struct {
	Name string
	// Head is the lane's tip, RemoteTip the tip of origin/<branch> ("" when the remote has no such branch).
	Head, RemoteTip string
	// Dirty says the tree has uncommitted or untracked files.
	Dirty bool
	// PR is the lane's pull request, nil when it has none.
	PR *PR
}

// mergedVerdict says whether a lane is removed: its PR is merged, its tip is on
// the remote (still on the branch, or as the head the PR merged) and its tree is
// clean. A lane that stays gets the reason.
func mergedVerdict(f mergedFacts) (remove bool, reason string) {
	switch {
	case f.Dirty:
		return false, "uncommitted changes in the tree"
	case f.PR == nil:
		return false, "no pull request found"
	case f.PR.State != "MERGED":
		return false, fmt.Sprintf("PR #%d is %s, not merged", f.PR.Number, strings.ToLower(f.PR.State))
	case f.Head != f.RemoteTip && f.Head != f.PR.Head:
		return false, fmt.Sprintf("commits not on the remote (tip %s, PR #%d merged %s)", short(f.Head), f.PR.Number, short(f.PR.Head))
	}
	return true, fmt.Sprintf("PR #%d merged", f.PR.Number)
}

// mergedFactsOf reads a lane's facts from git and GitHub.
func mergedFactsOf(lane Lane, prs map[string]*PR) mergedFacts {
	f := mergedFacts{Name: lane.Name}
	f.Head, _ = run(lane.Path, "git", "rev-parse", "HEAD")
	status, _ := run(lane.Path, "git", "status", "--porcelain")
	f.Dirty = strings.TrimSpace(status) != ""
	if lane.Branch != "" {
		f.RemoteTip, _ = run(lane.Path, "git", "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+lane.Branch)
		f.PR = prs[lane.Branch]
	}
	return f
}

func cmdRmMerged(repoName string, dryRun bool) error {
	repo, err := findRepo(repoName)
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(repo.Path, "")
	// The remote's view of the branches is the one that counts: prune what GitHub deleted.
	if _, err := runTimeout(repo.Path, 60*time.Second, "git", "fetch", "--quiet", "--prune", "origin"); err != nil {
		fmt.Printf("fetch failed (%v): judging by the last known remote state\n", err)
	}
	prs := prsOf(repo.Path, 0)
	if len(prs) == 0 {
		return fail("no pull requests read from GitHub (is gh signed in?): nothing is removed on a guess")
	}

	removed, kept := 0, 0
	for _, lane := range lanesOf(repo, loadState()) {
		if lane.IsMain || !lane.Managed {
			continue
		}
		remove, reason := mergedVerdict(mergedFactsOf(lane, prs))
		switch {
		case !remove:
			kept++
			fmt.Printf("keep    %-28s %s\n", lane.Name, reason)
		case dryRun:
			removed++
			fmt.Printf("remove  %-28s %s (dry run)\n", lane.Name, reason)
		default:
			fmt.Printf("remove  %-28s %s\n", lane.Name, reason)
			if err := removeLane(lane, cfg, false, true); err != nil {
				kept++
				fmt.Printf("keep    %-28s %v\n", lane.Name, err)
				continue
			}
			removed++
		}
	}
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	fmt.Printf("%s %d, kept %d\n", verb, removed, kept)
	return nil
}
