package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergedVerdict(t *testing.T) {
	merged := &PR{Number: 7, State: "MERGED", Head: "aaa1111"}
	cases := []struct {
		name   string
		facts  mergedFacts
		remove bool
		reason string
	}{
		{"merged, tip is the merged head, clean", mergedFacts{Head: "aaa1111", PR: merged}, true, "PR #7 merged"},
		{"merged, branch still on the remote at the tip", mergedFacts{Head: "aaa1111", RemoteTip: "aaa1111", PR: &PR{Number: 7, State: "MERGED", Head: "zzz"}}, true, "PR #7 merged"},
		{"dirty tree", mergedFacts{Head: "aaa1111", Dirty: true, PR: merged}, false, "uncommitted"},
		{"no pull request", mergedFacts{Head: "aaa1111"}, false, "no pull request"},
		{"open pull request", mergedFacts{Head: "aaa1111", PR: &PR{Number: 8, State: "OPEN", Head: "aaa1111"}}, false, "PR #8 is open"},
		{"closed unmerged", mergedFacts{Head: "aaa1111", PR: &PR{Number: 9, State: "CLOSED", Head: "aaa1111"}}, false, "not merged"},
		{"commits after the merge, never pushed", mergedFacts{Head: "bbb2222", RemoteTip: "aaa1111", PR: merged}, false, "not on the remote"},
		{"branch gone from the remote and tip is not the merged head", mergedFacts{Head: "bbb2222", PR: merged}, false, "not on the remote"},
	}
	for _, c := range cases {
		remove, reason := mergedVerdict(c.facts)
		if remove != c.remove || !strings.Contains(reason, c.reason) {
			t.Errorf("%s: got remove=%v reason=%q, want remove=%v containing %q", c.name, remove, reason, c.remove, c.reason)
		}
	}
}

func TestDescribeDBNeverPrintsSecrets(t *testing.T) {
	content := `# comment
NEXT_PUBLIC_SUPABASE_URL=https://abcdefghij.supabase.co
NEXT_PUBLIC_SUPABASE_ANON_KEY=sb_secret_KEYVALUE
export DATABASE_URL="postgresql://postgres.abcdefghij:hunter2PASS@aws-0-eu-west-1.pooler.supabase.com:6543/postgres?x=1"
PLANNING_DATABASE_URL='postgresql://postgres:pw@localhost:54322/postgres'
SERVICE_ROLE_KEY=nope
`
	got := strings.Join(dbTargetsIn(content), "\n")
	for _, secret := range []string{"hunter2PASS", "sb_secret", "KEYVALUE", "nope", "pw@", "x=1", "/postgres"} {
		if strings.Contains(got, secret) {
			t.Fatalf("output leaks %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		"NEXT_PUBLIC_SUPABASE_URL -> abcdefghij.supabase.co (project abcdefghij)",
		"DATABASE_URL -> aws-0-eu-west-1.pooler.supabase.com (project abcdefghij)",
		"PLANNING_DATABASE_URL -> localhost (local)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestDBTargetsReadsLinkedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps", "api", ".env.local"), []byte("DATABASE_URL=postgres://u:p@db.refref.supabase.co:5432/postgres\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := RepoConfig{Link: []string{"apps/api/.env.local", "apps/web/.env.local"}}
	got := dbTargets(Lane{Path: dir}, cfg)
	if len(got) != 1 || got[0] != "apps/api/.env.local: DATABASE_URL -> db.refref.supabase.co (project refref)" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandBase(t *testing.T) {
	cfg := RepoConfig{Base: "main"}
	if got := cfg.expand("steig lint --diff {base}", App{}, Lane{}); got != "steig lint --diff origin/main" {
		t.Fatal(got)
	}
}

// check exits non-zero (its error) when a repo-level check fails, and passes when they pass.
func TestCheckRunsRepoChecksAndFails(t *testing.T) {
	w := newLaneWorld(t)
	if err := os.WriteFile(filepath.Join(w.repo, "kitt.toml"), []byte("checks = [\"true\", \"echo boom; exit 3\", \"test {base} = origin/main\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, w.repo, "add", "kitt.toml")
	git(t, w.repo, "commit", "-q", "-m", "toml")
	git(t, w.repo, "push", "-q", "origin", "main")
	lane, err := findLane(w.repo)
	if err != nil {
		t.Fatal(err)
	}
	result, report := check(lane, false, "", false)
	if result.Passed != 2 || len(result.Failed) != 1 || result.Failed[0] != "repo: echo boom; exit 3" {
		t.Fatalf("result %+v\n%s", result, report)
	}
	if !strings.Contains(report, "boom") {
		t.Fatalf("report lacks the output: %s", report)
	}
	if _, report := check(lane, false, "api", false); !strings.Contains(report, "no checks to run") {
		t.Fatalf("--app should leave repo checks out: %s", report)
	}
}

// A fork branches from the lane's tip, its commits merge back, and rm needs no --force.
func TestForkAndRm(t *testing.T) {
	w := newLaneWorld(t)
	if err := w.new("ENG-5", "--branch", "tjark/eng-5-x"); err != nil {
		t.Fatal(err)
	}
	parent := w.lanePath("eng-5-x")
	git(t, parent, "commit", "-q", "--allow-empty", "-m", "lane work")
	tip := w.out(parent, "rev-parse", "HEAD")

	t.Chdir(parent)
	if err := cmdFork([]string{"slice one", "--no-setup"}); err != nil {
		t.Fatal(err)
	}
	fork := w.lanePath("eng-5-x-slice-one")
	if got := w.out(fork, "rev-parse", "--abbrev-ref", "HEAD"); got != "tjark/eng-5-x-slice-one" {
		t.Fatalf("fork is on %q", got)
	}
	if got := w.out(fork, "rev-parse", "HEAD"); got != tip {
		t.Fatalf("fork at %s, lane tip %s", got, tip)
	}
	if entry := loadState().Lanes[norm(fork)]; entry == nil || entry.ForkOf != "eng-5-x" {
		t.Fatalf("state %+v", entry)
	}

	// A commit only the fork has: rm refuses, until the lane has it.
	git(t, fork, "commit", "-q", "--allow-empty", "-m", "slice work")
	if err := cmdRm([]string{"eng-5-x-slice-one"}); err == nil || !strings.Contains(err.Error(), "not pushed or merged") {
		t.Fatalf("rm of a fork with its own commits: %v", err)
	}
	git(t, parent, "merge", "-q", "--ff-only", "tjark/eng-5-x-slice-one")
	if err := cmdRm([]string{"eng-5-x-slice-one"}); err != nil {
		t.Fatal(err)
	}
	if exists(fork) {
		t.Fatal("fork directory is still there")
	}
	if _, err := run(w.repo, "git", "rev-parse", "--verify", "--quiet", "refs/heads/tjark/eng-5-x-slice-one"); err == nil {
		t.Fatal("fork branch is still there")
	}
}

func TestHooksProblem(t *testing.T) {
	w := newLaneWorld(t)
	if got := hooksProblem(w.repo); got != "" {
		t.Fatalf("no hooksPath, got %q", got)
	}
	git(t, w.repo, "config", "core.hooksPath", ".husky/_")
	if got := hooksProblem(w.repo); !strings.Contains(got, "missing") {
		t.Fatalf("got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(w.repo, ".husky", "_"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := hooksProblem(w.repo); got != "" {
		t.Fatalf("got %q", got)
	}
}
