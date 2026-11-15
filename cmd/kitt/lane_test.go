package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// laneWorld is a kitt home with one registered repo, "r", cloned from a bare
// origin in the same temp dir, and herdr out of the way.
type laneWorld struct {
	t      *testing.T
	root   string
	origin string
	repo   string
	seed   string
}

func newLaneWorld(t *testing.T) *laneWorld {
	t.Helper()
	// The real path: on a Mac the temp dir is reached through a symlink, git prints the target.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITT_HOME", filepath.Join(root, "home"))
	t.Setenv("KITT_NO_HERDR", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "t")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "t@example.com")
	}
	off := false
	oldHerdr, oldBases := herdrFound, bases
	herdrFound, bases = &off, map[string]string{}
	t.Cleanup(func() { herdrFound, bases = oldHerdr, oldBases })

	w := &laneWorld{t: t, root: root, origin: filepath.Join(root, "origin.git"), repo: filepath.Join(root, "r"), seed: filepath.Join(root, "seed")}
	git(t, root, "init", "-q", "--bare", "-b", "main", w.origin)
	git(t, root, "init", "-q", "-b", "main", w.seed)
	if err := os.WriteFile(filepath.Join(w.seed, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, w.seed, "add", "a.txt")
	git(t, w.seed, "commit", "-q", "-m", "first")
	git(t, w.seed, "remote", "add", "origin", w.origin)
	git(t, w.seed, "push", "-q", "origin", "main")
	git(t, root, "clone", "-q", w.origin, w.repo)

	if err := saveGlobal(Global{Repos: []RepoRef{{Name: "r", Path: w.repo}}}); err != nil {
		t.Fatal(err)
	}
	return w
}

// new runs `kitt new` on the repo, without setup or an agent.
func (w *laneWorld) new(args ...string) error {
	return cmdNew(append(args, "--repo", "r", "--no-setup", "--no-agent"))
}

func (w *laneWorld) lanePath(name string) string {
	return filepath.Join(w.root, "r-lanes", name)
}

func (w *laneWorld) out(dir string, args ...string) string {
	w.t.Helper()
	out, err := run(dir, "git", args...)
	if err != nil {
		w.t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// A branch nobody has yet is started from origin's base, and pushes nowhere until told.
func TestNewBranchThatIsNew(t *testing.T) {
	w := newLaneWorld(t)
	if err := w.new("ENG-1", "--branch", "tjark/eng-1-fix-login"); err != nil {
		t.Fatal(err)
	}
	path := w.lanePath("eng-1-fix-login")
	if got := w.out(path, "rev-parse", "--abbrev-ref", "HEAD"); got != "tjark/eng-1-fix-login" {
		t.Fatalf("lane is on %q", got)
	}
	if up, err := run(path, "git", "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		t.Fatalf("a new branch tracks %q: it would push to the base", up)
	}
	entry := loadState().Lanes[norm(path)]
	if entry == nil || entry.Ticket != "ENG-1" || entry.Branch != "tjark/eng-1-fix-login" || entry.Name != "eng-1-fix-login" {
		t.Fatalf("state %+v", entry)
	}
}

// A branch begun in the repo is checked out as it is.
func TestNewBranchThatExistsLocally(t *testing.T) {
	w := newLaneWorld(t)
	git(t, w.repo, "branch", "tjark/eng-3-local")
	git(t, w.repo, "commit", "-q", "--allow-empty", "-m", "on main only")
	want := w.out(w.repo, "rev-parse", "tjark/eng-3-local")

	if err := w.new("ENG-3", "--branch", "tjark/eng-3-local"); err != nil {
		t.Fatal(err)
	}
	path := w.lanePath("eng-3-local")
	if got := w.out(path, "rev-parse", "--abbrev-ref", "HEAD"); got != "tjark/eng-3-local" {
		t.Fatalf("lane is on %q", got)
	}
	if got := w.out(path, "rev-parse", "HEAD"); got != want {
		t.Fatalf("lane is at %s, the branch at %s: it was started anew", got, want)
	}
}

// A branch that exists only on origin (work begun elsewhere) is checked out tracking it.
func TestNewBranchThatExistsOnOrigin(t *testing.T) {
	w := newLaneWorld(t)
	if err := os.WriteFile(filepath.Join(w.seed, "b.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, w.seed, "add", "b.txt")
	git(t, w.seed, "commit", "-q", "-m", "begun elsewhere")
	git(t, w.seed, "push", "-q", "origin", "main:refs/heads/tom/eng-2-remote")
	want := w.out(w.seed, "rev-parse", "HEAD")

	if err := w.new("ENG-2", "--branch", "tom/eng-2-remote"); err != nil {
		t.Fatal(err)
	}
	path := w.lanePath("eng-2-remote")
	if got := w.out(path, "rev-parse", "--abbrev-ref", "HEAD"); got != "tom/eng-2-remote" {
		t.Fatalf("lane is on %q", got)
	}
	if got := w.out(path, "rev-parse", "HEAD"); got != want {
		t.Fatalf("lane is at %s, origin's branch at %s", got, want)
	}
	if got := w.out(path, "rev-parse", "--abbrev-ref", "@{u}"); got != "origin/tom/eng-2-remote" {
		t.Fatalf("lane tracks %q, want origin/tom/eng-2-remote", got)
	}
}

func TestNewBranchThatIsNoBranchName(t *testing.T) {
	w := newLaneWorld(t)
	err := w.new("ENG-4", "--branch", "bad..name")
	if err == nil || !strings.Contains(err.Error(), "not a usable branch name") {
		t.Fatalf("err %v", err)
	}
	if exists(filepath.Join(w.root, "r-lanes")) {
		t.Fatal("a lane was begun on a bad name")
	}
}

// Two branches that end alike give one lane name: the second says whose it is and how to go on.
func TestNewLaneNameTaken(t *testing.T) {
	w := newLaneWorld(t)
	if err := w.new("ENG-1", "--branch", "tjark/eng-1-x"); err != nil {
		t.Fatal(err)
	}
	err := w.new("ENG-1", "--branch", "tom/eng-1-x")
	if err == nil || !strings.Contains(err.Error(), "tjark/eng-1-x") || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("err %v: it must name the lane's branch and --name", err)
	}
	if err := w.new("ENG-1", "--branch", "tom/eng-1-x", "--name", "eng-1-x-tom"); err != nil {
		t.Fatal(err)
	}
	if got := w.out(w.lanePath("eng-1-x-tom"), "rev-parse", "--abbrev-ref", "HEAD"); got != "tom/eng-1-x" {
		t.Fatalf("the named lane is on %q", got)
	}
}

// A lane is named by its ticket too, in any case: Linear writes ENG-123, a person eng-123.
func TestFindLaneByTicket(t *testing.T) {
	w := newLaneWorld(t)
	if err := w.new("ENG-123", "--branch", "tjark/eng-123-fix-login"); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"ENG-123", "eng-123", "eng-123-fix-login", "tjark/eng-123-fix-login"} {
		lane, err := findLane(arg)
		if err != nil || lane.Name != "eng-123-fix-login" {
			t.Errorf("findLane(%q) = %q, %v", arg, lane.Name, err)
		}
	}
	if _, err := findLane("ENG-12"); err == nil {
		t.Error("ENG-12 found the lane of ENG-123")
	}
}

func TestRepoAt(t *testing.T) {
	t.Setenv("KITT_HOME", t.TempDir())
	root := t.TempDir()
	a, b := filepath.Join(root, "work", "a"), filepath.Join(root, "other", "b")
	if err := saveGlobal(Global{Repos: []RepoRef{{Name: "a", Path: a}, {Name: "b", Path: b}}}); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{
		filepath.Join(a, "src", "app"): "a", // inside the checkout
		a:                              "a", // the checkout itself
		filepath.Join(root, "work"):    "a", // the folder above it, Linear's {{workDir}}
		filepath.Join(root, "other"):   "b",
	} {
		repo, err := repoAt(dir)
		if err != nil || repo.Name != want {
			t.Errorf("repoAt(%s) = %q, %v; want %s", dir, repo.Name, err, want)
		}
	}
	// A folder above two repos names neither, and one outside them all is no repo.
	if _, err := repoAt(root); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("repoAt above both: %v", err)
	}
	if _, err := repoAt(filepath.Join(root, "elsewhere")); err == nil || !strings.Contains(err.Error(), "no registered repo") {
		t.Errorf("repoAt outside: %v", err)
	}
}
