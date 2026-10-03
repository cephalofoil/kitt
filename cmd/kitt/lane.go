package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lane is one checkout of a repo that work happens in: the main checkout
// (slot 0) or a worktree. A managed lane holds a slot, which gives its apps
// their own ports.
type Lane struct {
	Repo    string
	Main    string
	Path    string
	Name    string
	Branch  string
	Head    string
	Slot    int
	IsMain  bool
	Managed bool
	State   *LaneState
}

func (l Lane) key() string { return norm(l.Path) }

// lanesOf lists a repo's worktrees as lanes, joined with what kitt remembers.
func lanesOf(repo RepoRef, state State) []Lane {
	out, err := run(repo.Path, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}

	var lanes []Lane
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		lane := Lane{Repo: repo.Name, Main: repo.Path, Slot: -1}
		skip := false
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				lane.Path = filepath.FromSlash(value)
			case "HEAD":
				lane.Head = value
			case "branch":
				lane.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "bare", "prunable":
				skip = true
			}
		}
		if lane.Path == "" || skip {
			continue
		}

		lane.Name = filepath.Base(lane.Path)
		if norm(lane.Path) == norm(repo.Path) {
			lane.IsMain, lane.Managed, lane.Slot, lane.Name = true, true, 0, "main"
		}
		if kept, ok := state.Lanes[lane.key()]; ok {
			lane.Managed, lane.Slot, lane.Name, lane.State = true, kept.Slot, kept.Name, kept
		}
		lanes = append(lanes, lane)
	}

	sort.SliceStable(lanes, func(i, j int) bool {
		a, b := lanes[i], lanes[j]
		if a.IsMain != b.IsMain {
			return a.IsMain
		}
		if a.Managed != b.Managed {
			return a.Managed
		}
		if a.Managed {
			return a.Slot < b.Slot
		}
		return a.Name < b.Name
	})

	return lanes
}

func allLanes() []Lane {
	state := loadState()
	var lanes []Lane
	for _, repo := range loadGlobal().Repos {
		lanes = append(lanes, lanesOf(repo, state)...)
	}
	return lanes
}

// findLane resolves what the person named: a lane name, repo/name, a path, or
// with nothing given, the lane the current directory is in.
func findLane(arg string) (Lane, error) {
	lanes := allLanes()
	if len(lanes) == 0 {
		return Lane{}, fail("no repos registered: run `kitt repo add` inside a repo")
	}

	if arg == "" || arg == "." {
		cwd, _ := os.Getwd()
		best := -1
		for i, lane := range lanes {
			if within(cwd, lane.Path) && (best < 0 || len(lane.Path) > len(lanes[best].Path)) {
				best = i
			}
		}
		if best < 0 {
			return Lane{}, fail("this directory is not in a registered repo: name a lane, or run `kitt repo add`")
		}
		return lanes[best], nil
	}

	var matches []Lane
	for _, lane := range lanes {
		if arg == lane.Name || arg == lane.Repo+"/"+lane.Name || arg == lane.Branch || norm(arg) == lane.key() {
			matches = append(matches, lane)
		}
	}
	if len(matches) == 0 && isNumber(arg) {
		for _, lane := range lanes {
			if lane.State != nil && strconv.Itoa(lane.State.Issue) == arg {
				matches = append(matches, lane)
			}
		}
	}
	switch len(matches) {
	case 0:
		return Lane{}, fail("no lane named %q", arg)
	case 1:
		return matches[0], nil
	default:
		return Lane{}, fail("%q names lanes in several repos: say <repo>/%s", arg, arg)
	}
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil && s != ""
}

// findRepo resolves the repo a command is about: the named one, the one the
// current directory is in, or the only one registered.
func findRepo(name string) (RepoRef, error) {
	repos := loadGlobal().Repos
	if name != "" {
		for _, repo := range repos {
			if repo.Name == name {
				return repo, nil
			}
		}
		return RepoRef{}, fail("no repo named %q: see `kitt repo list`", name)
	}
	if lane, err := findLane(""); err == nil {
		for _, repo := range repos {
			if repo.Name == lane.Repo {
				return repo, nil
			}
		}
	}
	if len(repos) == 1 {
		return repos[0], nil
	}
	if len(repos) == 0 {
		return RepoRef{}, fail("no repos registered: run `kitt repo add` inside a repo")
	}
	return RepoRef{}, fail("several repos are registered: pass --repo <name>")
}

// --- kitt repo ---------------------------------------------------------------

func cmdRepo(args []string) error {
	rest, _ := flags(args)
	sub := "list"
	if len(rest) > 0 {
		sub = rest[0]
	}
	g := loadGlobal()

	switch sub {
	case "add":
		dir := "."
		if len(rest) > 1 {
			dir = rest[1]
		}
		top, err := run(dir, "git", "rev-parse", "--show-toplevel")
		if err != nil {
			return fail("%s is not a git repository", dir)
		}
		// A worktree's toplevel is the worktree; the repo is where its common dir lives.
		if common, err := run(top, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			top = filepath.Dir(common)
		}
		top = filepath.Clean(filepath.FromSlash(top))
		cfg := loadRepoConfig(top, "")
		for _, repo := range g.Repos {
			if norm(repo.Path) == norm(top) {
				fmt.Printf("%s is already registered as %s\n", top, repo.Name)
				return nil
			}
		}
		g.Repos = append(g.Repos, RepoRef{Name: cfg.Name, Path: top})
		if err := saveGlobal(g); err != nil {
			return err
		}
		fmt.Printf("registered %s  %s\n", cfg.Name, top)
		describeRepo(cfg)
		return nil

	case "rm", "remove":
		if len(rest) < 2 {
			return fail("usage: kitt repo rm <name>")
		}
		kept := g.Repos[:0]
		for _, repo := range g.Repos {
			if repo.Name != rest[1] {
				kept = append(kept, repo)
			}
		}
		g.Repos = kept
		return saveGlobal(g)

	default:
		if len(g.Repos) == 0 {
			fmt.Println("no repos registered: run `kitt repo add` inside a repo")
		}
		for _, repo := range g.Repos {
			fmt.Printf("%s  %s\n", repo.Name, repo.Path)
			describeRepo(loadRepoConfig(repo.Path, ""))
		}
		return nil
	}
}

func describeRepo(cfg RepoConfig) {
	fmt.Printf("  config  %s\n  base    %s\n", cfg.Source, cfg.Base)
	for _, app := range cfg.Apps {
		shared := ""
		if app.Shared {
			shared = " shared"
		}
		fmt.Printf("  app     %-10s %-8s %-14s port %d%s · %d checks\n", app.Name, app.Kind, app.Dir, app.Port, shared, len(app.Checks))
	}
	fmt.Printf("  link    %d files\n", len(cfg.Link))
}

func cmdInit(args []string) error {
	_, opts := flags(args, "force")
	repo, err := findRepo(opts["repo"])
	if err != nil {
		return err
	}
	path := filepath.Join(repo.Path, "kitt.toml")
	if exists(path) && opts["force"] == "" {
		return fail("%s exists: pass --force to overwrite it", path)
	}
	cfg := loadRepoConfig(repo.Path, "")
	if err := os.WriteFile(path, []byte(renderConfig(cfg)), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s from what the checkout shows; edit it to fit\n", path)
	return nil
}

// --- kitt new ----------------------------------------------------------------

func cmdNew(args []string) error {
	rest, opts := flags(args, "agent", "no-agent", "focus", "no-setup")
	if len(rest) == 0 {
		return fail("usage: kitt new <issue number | name> [--repo r] [--base ref] [--agent | --no-agent] [--prompt text] [--focus]")
	}
	repo, err := findRepo(opts["repo"])
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(repo.Path, "")

	entry := LaneState{Repo: repo.Name, Created: time.Now()}
	name := slug(strings.Join(rest, " "), 40)
	if isNumber(rest[0]) {
		var issue struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		out, err := run(repo.Path, "gh", "issue", "view", rest[0], "--json", "title,url")
		if err != nil {
			return fail("issue #%s: %v", rest[0], err)
		}
		_ = json.Unmarshal([]byte(out), &issue)
		entry.Issue, _ = strconv.Atoi(rest[0])
		entry.Title, entry.URL = issue.Title, issue.URL
		name = rest[0] + "-" + slug(issue.Title, 32)
	}
	if name == "" {
		return fail("that gives no usable lane name")
	}
	entry.Name = name

	path := filepath.Join(cfg.lanesDir(repo.Path), name)
	branch := cfg.BranchPrefix + name
	if exists(path) {
		return fail("%s exists already", path)
	}
	base := opts["base"]
	if base == "" {
		fmt.Printf("fetching origin/%s\n", cfg.Base)
		if _, err := runTimeout(repo.Path, 60*time.Second, "git", "fetch", "--quiet", "origin", cfg.Base); err != nil {
			fmt.Printf("  fetch failed (%v): branching from the last known origin/%s\n", err, cfg.Base)
		}
		base = "origin/" + cfg.Base
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	workspace := ""
	if hasHerdr() {
		var result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
		}
		args := []string{"worktree", "create", "--cwd", repo.Path, "--branch", branch, "--base", base,
			"--path", filepath.ToSlash(path), "--label", name}
		if opts["focus"] != "" {
			args = append(args, "--focus")
		} else {
			args = append(args, "--no-focus")
		}
		if err := herdrTimeout(&result, 60*time.Second, args...); err != nil {
			fmt.Printf("  herdr could not create it (%v): using git\n", err)
		} else {
			workspace = result.Workspace.ID
		}
	}
	if !exists(path) {
		if _, err := runTimeout(repo.Path, 60*time.Second, "git", "worktree", "add", "-b", branch, path, base); err != nil {
			return err
		}
	}
	// A lane starts from origin's base but must not push to it by accident.
	_, _ = run(path, "git", "branch", "--unset-upstream")

	entry.Path = path
	if err := updateState(func(s *State) {
		entry.Slot = s.freeSlot(repo.Name)
		s.Lanes[norm(path)] = &entry
	}); err != nil {
		return err
	}
	lane := Lane{Repo: repo.Name, Main: repo.Path, Path: path, Name: name, Branch: branch, Slot: entry.Slot, Managed: true, State: &entry}

	fmt.Printf("lane %s  slot %d  %s\n  %s\n", name, lane.Slot, branch, path)
	linkFiles(lane, cfg, os.Stdout)
	if opts["no-setup"] == "" {
		setupLane(lane, cfg)
	}
	for _, app := range cfg.Apps {
		if app.Port > 0 {
			fmt.Printf("  %-8s port %d\n", app.Name, app.port(lane.Slot))
		}
	}

	// A lane made for an issue is made to be worked on: the agent starts unless told not to.
	wantsAgent := opts["agent"] != "" || opts["prompt"] != "" || (entry.Issue > 0 && opts["no-agent"] == "")
	if !wantsAgent {
		return nil
	}
	if workspace == "" {
		return fail("an agent needs herdr: start one in %s yourself", path)
	}
	return startAgent(lane, cfg, opts["prompt"])
}

const issuePrompt = "Work on issue #{issue}: {title}. Read it first with `gh issue view {issue} --comments`. " +
	"This checkout is a kitt lane: run `kitt env` for its ports, `kitt check` before you report, " +
	"and when the change is visible in the app, record a proof (`kitt proof begin`, `shot`, `end`)."

// startAgent puts an agent to work in a lane: it starts one in the lane's shell
// pane unless one is already there, then hands it the prompt, or for an issue
// lane with no prompt given, the issue.
func startAgent(lane Lane, cfg RepoConfig, prompt string) error {
	workspace, err := herdrOpen(lane, false)
	if err != nil || workspace == "" {
		return fail("could not open %s in herdr: %v", lane.Name, err)
	}

	target := ""
	for _, agent := range herdrAgents() {
		if agent.WorkspaceID == workspace && within(agent.Cwd, lane.Path) {
			target = agent.PaneID
		}
	}
	if target == "" {
		devTabs := map[string]bool{}
		for _, tab := range herdrTabs(workspace) {
			if strings.HasPrefix(tab.Label, "dev:") {
				devTabs[tab.TabID] = true
			}
		}
		pane := ""
		for _, candidate := range herdrPanes(workspace) {
			if !devTabs[candidate.TabID] {
				pane = candidate.PaneID
				break
			}
		}
		if pane == "" {
			return fail("workspace %s has no shell pane to start an agent in", workspace)
		}
		name := agentName(lane.Name)
		fmt.Printf("starting %s as %q\n", cfg.Agent.Kind, name)
		if err := herdrTimeout(nil, 90*time.Second, "agent", "start", name, "--kind", cfg.Agent.Kind, "--pane", pane, "--timeout", "60000"); err != nil {
			return err
		}
		target = pane
	}

	if prompt == "" && lane.State != nil && lane.State.Issue > 0 {
		prompt = cfg.Agent.Prompt
		if prompt == "" {
			prompt = issuePrompt
		}
		prompt = strings.NewReplacer("{issue}", strconv.Itoa(lane.State.Issue), "{title}", lane.State.Title, "{url}", lane.State.URL).Replace(prompt)
	}
	if prompt == "" {
		fmt.Printf("agent ready in %s\n", lane.Name)
		return nil
	}
	if err := herdr(nil, "agent", "prompt", target, prompt); err != nil {
		return err
	}
	fmt.Printf("agent in %s is on it\n", lane.Name)
	return nil
}

// cmdAgent starts (or prompts) the agent of an existing lane.
func cmdAgent(args []string) error {
	rest, opts := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	if !hasHerdr() {
		return fail("an agent needs herdr")
	}
	return startAgent(lane, loadRepoConfig(lane.Main, lane.Path), opts["prompt"])
}

// linkFiles gives a lane the gitignored files of the main checkout as symlinks,
// so an edit to an env file reaches every lane.
func linkFiles(lane Lane, cfg RepoConfig, out io.Writer) {
	linked, kept := 0, 0
	for _, rel := range cfg.Link {
		target := filepath.Join(lane.Main, filepath.FromSlash(rel))
		link := filepath.Join(lane.Path, filepath.FromSlash(rel))
		if !exists(target) {
			continue
		}
		if info, err := os.Lstat(link); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if dest, _ := os.Readlink(link); norm(dest) == norm(target) {
					kept++
					continue
				}
				_ = os.Remove(link)
			} else {
				fmt.Fprintf(out, "  link    %s is a real file in the lane: left alone\n", rel)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			fmt.Fprintf(out, "  link    %s: %v\n", rel, err)
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			// Without Developer Mode Windows refuses symlinks: a hard link still
			// shares the content, a copy at least starts the lane.
			if os.Link(target, link) == nil {
				fmt.Fprintf(out, "  link    %s: hard link (symlinks need Developer Mode)\n", rel)
			} else if data, readErr := os.ReadFile(target); readErr == nil && os.WriteFile(link, data, 0o600) == nil {
				fmt.Fprintf(out, "  link    %s: COPIED, it will not follow the main checkout\n", rel)
			} else {
				fmt.Fprintf(out, "  link    %s: %v\n", rel, err)
				continue
			}
		}
		linked++
	}
	fmt.Fprintf(out, "  link    %d linked, %d already in place\n", linked, kept)
}

// setupLane prepares a fresh lane: the repo's setup lines, then each app's own,
// except the apps marked lazy, which are set up when first started or checked.
func setupLane(lane Lane, cfg RepoConfig) {
	for _, line := range cfg.Setup {
		runSetup(lane.Path, cfg.expand(line, App{}, lane))
	}
	for _, app := range cfg.Apps {
		if !app.Lazy {
			ensureSetup(lane, cfg, app)
		}
	}
}

func runSetup(dir, line string) bool {
	fmt.Printf("  setup   %s  (%s)\n", line, filepath.Base(dir))
	cmd := shell(dir, line)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  setup   failed: %v\n", err)
		return false
	}
	return true
}

// ensureSetup runs an app's setup lines once per lane. The main checkout and
// shared apps are the person's own to set up.
func ensureSetup(lane Lane, cfg RepoConfig, app App) {
	if lane.IsMain || app.Shared || len(app.Setup) == 0 {
		return
	}
	if entry := loadState().Lanes[lane.key()]; entry != nil {
		for _, done := range entry.Setup {
			if done == app.Name {
				return
			}
		}
	}
	dir := filepath.Join(lane.Path, filepath.FromSlash(app.Dir))
	for _, line := range app.Setup {
		if !runSetup(dir, cfg.expand(line, app, lane)) {
			return
		}
	}
	_ = updateState(func(s *State) {
		if entry := s.Lanes[lane.key()]; entry != nil {
			entry.Setup = append(entry.Setup, app.Name)
		}
	})
}

// --- kitt adopt / link / rm --------------------------------------------------

// cmdAdopt takes a worktree kitt did not create (Claude's, herdr's, a hand-made
// one) as a lane: it gets a slot and the linked files.
func cmdAdopt(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	if lane.IsMain {
		return fail("the main checkout is always a lane")
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	if !lane.Managed {
		entry := LaneState{Repo: lane.Repo, Name: lane.Name, Path: lane.Path, Created: time.Now()}
		if err := updateState(func(s *State) {
			entry.Slot = s.freeSlot(lane.Repo)
			s.Lanes[lane.key()] = &entry
		}); err != nil {
			return err
		}
		lane.Slot, lane.Managed = entry.Slot, true
	}
	fmt.Printf("lane %s  slot %d  %s\n", lane.Name, lane.Slot, lane.Path)
	linkFiles(lane, cfg, os.Stdout)
	return nil
}

func cmdLink(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	if lane.IsMain {
		return fail("the main checkout holds the real files")
	}
	linkFiles(lane, loadRepoConfig(lane.Main, lane.Path), os.Stdout)
	return nil
}

func cmdRm(args []string) error {
	rest, opts := flags(args, "force")
	if len(rest) == 0 {
		return fail("usage: kitt rm <lane> [--force]")
	}
	lane, err := findLane(rest[0])
	if err != nil {
		if cleaned := removeLeftover(rest[0]); cleaned != "" {
			fmt.Println(cleaned)
			return nil
		}
		return err
	}
	if lane.IsMain {
		return fail("the main checkout is not removed")
	}
	force := opts["force"] != ""
	cfg := loadRepoConfig(lane.Main, lane.Path)

	if !force {
		if reason := unsafeToRemove(lane, cfg); reason != "" {
			return fail("%s: %s (pass --force to remove it anyway)", lane.Name, reason)
		}
	}

	// Whatever the lane started (a container on its port) goes before its directory does.
	down(lane)

	// The linked files are symlinks into the main checkout: take the links away
	// first so no removal can follow one.
	for _, rel := range cfg.Link {
		link := filepath.Join(lane.Path, filepath.FromSlash(rel))
		if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
		}
	}

	if ws := herdrWorkspaceOf(lane.Main)[lane.key()]; ws != "" {
		_ = herdr(nil, "workspace", "close", ws)
	}
	if err := removeWorktree(lane, force); err != nil {
		return err
	}
	_ = updateState(func(s *State) { delete(s.Lanes, lane.key()) })

	// -d only deletes what is merged; a squash-merged branch stays and says so.
	if lane.Branch != "" {
		if _, err := run(lane.Main, "git", "branch", "-d", lane.Branch); err != nil {
			fmt.Printf("removed %s; branch %s kept (not merged into %s as git sees it)\n", lane.Name, lane.Branch, cfg.Base)
			return nil
		}
	}
	fmt.Printf("removed %s and its branch\n", lane.Name)
	return nil
}

// removeWorktree takes a lane's checkout away. Git does it when it can; on
// Windows it gives up on the long paths inside node_modules, and then the
// directory is deleted directly and git's record of it pruned.
func removeWorktree(lane Lane, force bool) error {
	removal := []string{"worktree", "remove", lane.Path}
	if force {
		removal = append(removal, "--force")
	}
	_, gitErr := runTimeout(lane.Main, 120*time.Second, "git", removal...)
	if gitErr == nil && !exists(lane.Path) {
		return nil
	}

	// Only ever a worktree of this repo, never the repo itself.
	if lane.IsMain || norm(lane.Path) == norm(lane.Main) || within(lane.Main, lane.Path) {
		return gitErr
	}
	if err := os.RemoveAll(lane.Path); err != nil {
		return fail("could not delete %s: %v", lane.Path, err)
	}
	_, _ = run(lane.Main, "git", "worktree", "prune")
	return nil
}

// removeLeftover finishes a removal git only half did: the worktree is gone
// from git, but its directory and kitt's entry for it are still there.
func removeLeftover(name string) string {
	state := loadState()
	for key, entry := range state.Lanes {
		if entry.Name != name && entry.Repo+"/"+entry.Name != name {
			continue
		}
		repo, err := findRepo(entry.Repo)
		if err != nil {
			return ""
		}
		cfg := loadRepoConfig(repo.Path, "")
		// Only a directory under the repo's lanes folder is ever deleted this way.
		if !within(entry.Path, cfg.lanesDir(repo.Path)) || norm(entry.Path) == norm(cfg.lanesDir(repo.Path)) {
			return ""
		}
		if err := os.RemoveAll(entry.Path); err != nil {
			return fmt.Sprintf("could not delete %s: %v", entry.Path, err)
		}
		_, _ = run(repo.Path, "git", "worktree", "prune")
		_ = updateState(func(s *State) { delete(s.Lanes, key) })
		if _, err := run(repo.Path, "git", "branch", "-d", cfg.BranchPrefix+entry.Name); err != nil {
			return fmt.Sprintf("removed what was left of %s; its branch is kept", entry.Name)
		}
		return fmt.Sprintf("removed what was left of %s and its branch", entry.Name)
	}
	return ""
}

// unsafeToRemove says why a lane still holds work that exists nowhere else.
func unsafeToRemove(lane Lane, cfg RepoConfig) string {
	status, _ := run(lane.Path, "git", "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		return "it has uncommitted changes"
	}
	if lane.Branch == "" {
		return ""
	}
	ahead, _ := run(lane.Path, "git", "rev-list", "--count", "origin/"+cfg.Base+"..HEAD")
	if ahead == "0" || ahead == "" {
		return ""
	}
	if pr := prOf(lane.Main, lane.Branch); pr != nil && pr.State == "MERGED" && pr.Head == lane.Head {
		return ""
	}
	if unpushed, err := run(lane.Path, "git", "rev-list", "--count", "@{upstream}..HEAD"); err == nil && unpushed == "0" {
		return ""
	}
	return fmt.Sprintf("it has %s commits that are not pushed or merged", ahead)
}
