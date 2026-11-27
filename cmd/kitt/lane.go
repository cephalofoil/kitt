package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
			lane.IsMain, lane.Managed, lane.Slot = true, true, 0
		}
		if kept, ok := state.Lanes[lane.key()]; ok {
			lane.Managed, lane.Slot, lane.Name, lane.State = true, kept.Slot, kept.Name, kept
		}
		if lane.IsMain {
			// The repo's own checkout goes by the name of its base branch.
			lane.Name = baseOf(repo.Path)
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
		ticket := lane.State != nil && lane.State.Ticket != "" && strings.EqualFold(arg, lane.State.Ticket)
		if arg == lane.Name || arg == lane.Repo+"/"+lane.Name || arg == lane.Branch || norm(arg) == lane.key() || (lane.IsMain && arg == "main") || ticket {
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

// repoAt resolves a directory to the registered repo it lies in, or that lies
// in it (a tracker's working directory may be the folder above the checkout).
func repoAt(dir string) (RepoRef, error) {
	var found []RepoRef
	for _, repo := range loadGlobal().Repos {
		if within(dir, repo.Path) {
			return repo, nil
		}
		if within(repo.Path, dir) {
			found = append(found, repo)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return RepoRef{}, fail("several registered repos lie in %s: pass --repo <name>", dir)
	}
	return RepoRef{}, fail("%s is in no registered repo: run `kitt repo add` there", dir)
}

var bases = map[string]string{}

// baseOf is a repo's base branch, read once per run.
func baseOf(repoPath string) string {
	if base, ok := bases[repoPath]; ok {
		return base
	}
	bases[repoPath] = loadRepoConfig(repoPath, "").Base
	return bases[repoPath]
}

// wants says whether an app is one of those the lane runs by default: the ones
// it was made for, or with none named, every app that is not lazy.
func (l Lane) wants(app App) bool {
	if l.State == nil || len(l.State.Apps) == 0 {
		return !app.Lazy
	}
	for _, name := range l.State.Apps {
		// What the lane's app talks to runs along: a shared backend, a database.
		if name == app.Name || (app.Shared && !app.Lazy) {
			return true
		}
	}
	return false
}

// hasExpo says whether the lane runs a phone app, which is what the emulator is for.
func (l Lane) hasExpo(cfg RepoConfig) bool {
	for _, app := range cfg.Apps {
		if app.Kind == "expo" && l.wants(app) {
			return true
		}
	}
	return false
}

// --- kitt repo ---------------------------------------------------------------

func cmdRepo(args []string) error {
	rest, opts := flags(args, "json")
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
		if cfg.Source == "detected" {
			fmt.Println("\nThis is what kitt detects; nothing was written. To change it:")
			fmt.Println("  kitt init           walks you through it and writes kitt.toml")
			fmt.Println("  kitt init --agent   lets an agent read the repo first for dev commands, checks and ports")
		}
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
		if opts["json"] != "" {
			type repoRow struct {
				Name string `json:"name"`
				Path string `json:"path"`
				// Agent is the kind a new lane's agent starts as ([agent] kind).
				Agent string `json:"agent"`
			}
			list := []repoRow{}
			for _, repo := range g.Repos {
				list = append(list, repoRow{repo.Name, repo.Path, loadRepoConfig(repo.Path, "").Agent.Kind})
			}
			return json.NewEncoder(os.Stdout).Encode(list)
		}
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

// --- kitt new ----------------------------------------------------------------

func cmdNew(args []string) error {
	rest, opts := flags(args, "agent", "blank", "no-agent", "focus", "no-setup", "up", "force")
	var apps []string
	if opts["apps"] != "" {
		apps = strings.Split(opts["apps"], ",")
	}
	if len(rest) == 0 {
		return fail("usage: kitt new <issue number | ticket | name> [--repo r | --dir d] [--base ref] [--branch b] [--name lane] [--apps web,admin] [--agent | --blank | --no-agent] [--prompt text | --prompt-env VAR] [--up [--force]] [--focus]")
	}
	repo, err := findRepo(opts["repo"])
	if opts["dir"] != "" && opts["repo"] == "" {
		repo, err = repoAt(opts["dir"])
	}
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(repo.Path, "")
	if opts["with"] != "" {
		if err := checkAgentKind(opts["with"]); err != nil {
			return err
		}
		cfg.Agent.Kind = opts["with"]
	}

	prompt := opts["prompt"]
	if opts["prompt-env"] != "" {
		prompt = os.Getenv(opts["prompt-env"])
	}
	// A branch given from outside (Linear's name for the ticket) replaces branch_prefix + name.
	branch := opts["branch"]
	if branch != "" {
		if _, err := run(repo.Path, "git", "check-ref-format", "--branch", branch); err != nil {
			return fail("%q is not a usable branch name", branch)
		}
		// A branch that already has a lane is that lane: opening the ticket again goes back to it.
		for _, lane := range lanesOf(repo, loadState()) {
			if lane.Branch != branch {
				continue
			}
			if !lane.Managed {
				return fail("%s is checked out at %s, which is not a lane yet: kitt adopt %s", branch, lane.Path, lane.Path)
			}
			fmt.Printf("%s is the lane %s\n", branch, lane.Name)
			// An agent already at work on it is not handed the ticket a second time.
			busy := false
			for _, agent := range herdrAgents() {
				busy = busy || within(agent.Cwd, lane.Path)
			}
			if !busy && (prompt != "" || opts["agent"] != "" || opts["blank"] != "") && hasHerdr() {
				pane, err := openAgent(lane, cfg)
				if err != nil {
					return err
				}
				if opts["blank"] == "" {
					prompt = agentPrompt(lane, cfg, prompt)
				}
				if err := promptAgent(lane, pane, prompt); err != nil {
					return err
				}
			}
			if opts["up"] != "" {
				started, _, err := upApps(lane, nil, false, opts["force"] != "")
				for _, line := range started {
					fmt.Println("  " + line)
				}
				if err != nil {
					return err
				}
			}
			if opts["focus"] != "" && hasHerdr() {
				_, err := herdrOpen(lane, true)
				return err
			}
			return nil
		}
	}

	for _, name := range apps {
		if cfg.app(name) == nil {
			return fail("%s has no app named %q", repo.Name, name)
		}
	}
	entry := LaneState{Repo: repo.Name, Created: time.Now(), Apps: apps, Prompt: prompt, AgentKind: opts["with"]}
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
	} else if isTicket(rest[0], branch != "" || opts["prompt-env"] != "") {
		entry.Ticket = rest[0]
	}
	if branch != "" {
		// The branch says more than a ticket id: tjark/eng-123-fix-login is the lane eng-123-fix-login.
		if fromBranch := slug(branch[strings.LastIndex(branch, "/")+1:], 40); fromBranch != "" && !isNumber(rest[0]) {
			name = fromBranch
		}
		entry.Branch = branch
	}
	// Two branches can end alike (tjark/eng-1-x, tom/eng-1-x): --name tells their lanes apart.
	if opts["name"] != "" {
		name = slug(opts["name"], 40)
	}
	if branch == "" {
		branch = cfg.BranchPrefix + name
	}
	if name == "" {
		return fail("that gives no usable lane name")
	}
	entry.Name = name

	path := filepath.Join(cfg.lanesDir(repo.Path), name)
	if exists(path) {
		for _, lane := range lanesOf(repo, loadState()) {
			if lane.key() == norm(path) {
				return fail("the lane %s exists already, on the branch %s: give this one a name of its own with --name <lane>", name, lane.Branch)
			}
		}
		return fail("%s exists already: give the lane another name with --name <lane>", path)
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

	// A given branch may exist already, here or on origin (work on the ticket begun
	// elsewhere): then the lane checks it out instead of starting it anew.
	existing := ""
	if entry.Branch != "" {
		if _, err := run(repo.Path, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			existing = branch
		} else if _, err := runTimeout(repo.Path, 60*time.Second, "git", "fetch", "--quiet", "origin", branch+":refs/remotes/origin/"+branch); err == nil {
			existing = "origin/" + branch
		}
	}

	if existing == "" && hasHerdr() {
		if _, err := herdrNow().newWorktree(repo.Path, branch, base, path, name, opts["focus"] != ""); err != nil {
			fmt.Printf("  %s could not create it (%v): using git\n", herdrName(), err)
		}
	}
	if !exists(path) {
		gitArgs := []string{"worktree", "add", "-b", branch, path, base}
		switch {
		case existing == branch:
			gitArgs = []string{"worktree", "add", path, branch}
		case existing != "":
			gitArgs = []string{"worktree", "add", "--track", "-b", branch, path, existing}
		}
		if _, err := runTimeout(repo.Path, 90*time.Second, "git", gitArgs...); err != nil {
			return err
		}
	}
	if existing == "" {
		// A lane starts from origin's base but must not push to it by accident.
		_, _ = run(path, "git", "branch", "--unset-upstream")
	}

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

	// A lane made for an issue is made to be worked on: the agent starts unless told not to.
	wantsAgent := opts["agent"] != "" || opts["blank"] != "" || prompt != "" || (entry.Issue > 0 && opts["no-agent"] == "")
	// The agent opens before the install, which takes minutes: it can be talked
	// to meanwhile. Its prompt waits, so it does not install over the install.
	// An agent that does not open leaves the lane set up all the same.
	pane, agentErr := "", error(nil)
	if wantsAgent && hasHerdr() {
		pane, agentErr = openAgent(lane, cfg)
	}
	if opts["no-setup"] == "" {
		setupLane(lane, cfg)
	}
	for _, app := range cfg.Apps {
		if app.Port > 0 {
			fmt.Printf("  %-8s port %d\n", app.Name, app.port(lane.Slot))
		}
	}
	if agentErr != nil {
		return agentErr
	}

	if wantsAgent {
		if !hasHerdr() {
			return fail("an agent needs herdr: start one in %s yourself", path)
		}
		// --blank leaves out the issue (or stored) prompt; a prompt given still goes.
		if opts["blank"] == "" {
			prompt = agentPrompt(lane, cfg, prompt)
		}
		if err := promptAgent(lane, pane, prompt); err != nil {
			return err
		}
	}
	if opts["up"] != "" {
		// The agent has the lane's shell pane, the dev servers get a tab of their own.
		started, _, err := upApps(lane, nil, false, opts["force"] != "")
		for _, line := range started {
			fmt.Println("  " + line)
		}
		if err != nil {
			return err
		}
	}
	if opts["focus"] != "" && hasHerdr() {
		_, err := herdrOpen(lane, true)
		return err
	}
	return nil
}

// ticketID is a tracker's identifier for an issue, as Linear writes it: ENG-123.
var ticketID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

// isTicket says whether kitt new's name is a ticket. Given alone, its team key
// is upper case (ENG-123), so fix-2 or v2-3 stay names; from a tracker (a
// --branch or --prompt-env, as kitt-linear passes) any case is a ticket.
func isTicket(arg string, fromTracker bool) bool {
	if !ticketID.MatchString(arg) {
		return false
	}
	key := arg[:strings.Index(arg, "-")]
	return fromTracker || key == strings.ToUpper(key)
}

const issuePrompt = "Implement issue #{issue}: {title}. Read it first with `gh issue view {issue} --comments`. " +
	"This checkout is a kitt lane: `kitt env` prints its ports. The goal is a pull request that is ready to be merged, " +
	"with the change seen working in the app. Build it, run `kitt check` and fix what fails, commit. " +
	"Then prove it in the running app: `kitt proof begin`, `kitt proof shot <label>` for each state worth showing, " +
	"`kitt proof end --pass` (or `--fail --note` and back to work). A change no app shows needs no proof: say so. " +
	"After the proof, push and open the pull request with `gh pr create`: its body closes the issue (`Closes #{issue}`) " +
	"and says what the proof shows. Do not merge it. Where the issue leaves open what to build, ask before building on a guess."

// startAgent puts an agent to work in a lane: it starts one in the lane's shell
// pane unless one is already there, then hands it the prompt, or for an issue
// lane with no prompt given, the issue.
func startAgent(lane Lane, cfg RepoConfig, prompt string) error {
	pane, err := openAgent(lane, cfg)
	if err != nil {
		return err
	}
	return promptAgent(lane, pane, agentPrompt(lane, cfg, prompt))
}

// openAgent starts an agent in the lane's shell pane unless one is already
// there, and returns the pane it is in.
func openAgent(lane Lane, cfg RepoConfig) (string, error) {
	workspace, err := herdrOpen(lane, false)
	if err != nil || workspace == "" {
		return "", fail("could not open %s in %s: %v", lane.Name, herdrName(), err)
	}

	// An agent of the lane's kind at work is the one; one of another kind keeps
	// its pane, and this one gets a tab of its own.
	target, other := "", false
	for _, agent := range herdrAgents() {
		if agent.WorkspaceID == workspace && within(agent.Cwd, lane.Path) {
			if agentIs(agent, cfg.Agent.Kind) {
				target = agent.PaneID
			} else {
				other = true
			}
		}
	}
	if target == "" {
		name, tab := agentName(lane.Name), ""
		if other {
			tab = cfg.Agent.Kind
		}
		fmt.Printf("starting %s as %q\n", cfg.Agent.Kind, name)
		env, err := agentEnv(cfg.Agent.Kind)
		if err != nil {
			return "", err
		}
		if target, err = herdrNow().startAgent(workspace, lane, cfg.Agent.Kind, name, tab, env); err != nil {
			return "", err
		}
	}
	return target, nil
}

// anotherAgent starts one more agent in a lane, in a tab of its own: the one
// at work stays at it. It returns the pane the new one is in.
func anotherAgent(lane Lane, cfg RepoConfig) (string, error) {
	workspace, err := herdrOpen(lane, false)
	if err != nil || workspace == "" {
		return "", fail("could not open %s in %s: %v", lane.Name, herdrName(), err)
	}
	count := 1
	for _, agent := range herdrAgents() {
		if agent.WorkspaceID == workspace && within(agent.Cwd, lane.Path) {
			count++
		}
	}
	label := fmt.Sprintf("%s %d", cfg.Agent.Kind, count)
	// Two agents of a lane are told apart by a number, inside what herdr takes for a name.
	name, suffix := agentName(lane.Name), fmt.Sprintf("-%d", count)
	if len(name)+len(suffix) > 32 {
		name = strings.TrimRight(name[:32-len(suffix)], "-")
	}
	name += suffix
	fmt.Printf("starting %s as %q\n", cfg.Agent.Kind, name)
	env, err := agentEnv(cfg.Agent.Kind)
	if err != nil {
		return "", err
	}
	return herdrNow().startAgent(workspace, lane, cfg.Agent.Kind, name, label, env)
}

// agentPrompt is what a lane's agent is told: the prompt given, else the one
// the lane was made with, else for an issue lane, the issue.
func agentPrompt(lane Lane, cfg RepoConfig, prompt string) string {
	if prompt != "" || lane.State == nil {
		return prompt
	}
	// What the agent was given when the lane was made (Linear's prompt) is given again.
	if lane.State.Prompt != "" {
		return lane.State.Prompt
	}
	if lane.State.Issue == 0 {
		return ""
	}
	prompt = cfg.Agent.Prompt
	if prompt == "" {
		prompt = issuePrompt
	}
	return strings.NewReplacer("{issue}", strconv.Itoa(lane.State.Issue), "{title}", lane.State.Title, "{url}", lane.State.URL).Replace(prompt)
}

// promptAgent hands the agent in a pane its prompt; with none it is left blank.
func promptAgent(lane Lane, target, prompt string) error {
	if prompt == "" {
		fmt.Printf("agent ready in %s\n", lane.Name)
		return nil
	}
	if err := herdrNow().prompt(target, prompt); err != nil {
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
	cfg := loadRepoConfig(lane.Main, lane.Path)
	if err := withAgentKind(lane, &cfg, opts["with"]); err != nil {
		return err
	}
	return startAgent(lane, cfg, opts["prompt"])
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

// setupLane prepares a fresh lane: the repo's setup lines, then the setup of
// the apps the lane is about. The others are set up when first started or checked.
func setupLane(lane Lane, cfg RepoConfig) {
	for _, line := range cfg.Setup {
		runSetup(lane.Path, cfg.expand(line, App{}, lane))
	}
	for _, app := range cfg.Apps {
		if lane.wants(app) {
			ensureSetup(lane, cfg, app)
		}
	}
}

// --- Setup: installing a lane's dependencies -----------------------------------

// Two installs at once fight over the package manager's cache (EBUSY on
// Windows), so setup runs one lane at a time, whichever kitt process starts it.
type setupLock struct {
	Lane string    `json:"lane"`
	At   time.Time `json:"at"`
}

const setupStale = 20 * time.Minute

func setupLockPath() string { return filepath.Join(configDir(), "setup.lock") }

// setupHolder is the lane installing right now, or "".
func setupHolder() string {
	var held setupLock
	if readJSON(setupLockPath(), &held) != nil || time.Since(held.At) > setupStale {
		return ""
	}
	return held.Lane
}

// acquireSetup waits for the install lock and returns what releases it.
func acquireSetup(lane Lane) func() {
	path := setupLockPath()
	_ = os.MkdirAll(configDir(), 0o755)
	announced := false
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = json.NewEncoder(file).Encode(setupLock{Lane: lane.key(), At: time.Now()})
			file.Close()
			return func() { _ = os.Remove(path) }
		}
		var held setupLock
		if readJSON(path, &held) != nil {
			// Being written this instant, or broken: look again before judging it.
			time.Sleep(300 * time.Millisecond)
			if readJSON(path, &held) != nil {
				_ = os.Remove(path)
			}
			continue
		}
		if time.Since(held.At) > setupStale {
			_ = os.Remove(path)
			continue
		}
		if !announced {
			fmt.Printf("  setup   waiting: another lane is installing (%s)\n", filepath.Base(held.Lane))
			announced = true
		}
		time.Sleep(2 * time.Second)
	}
}

// runSetup runs one setup line. A file another process holds for a moment
// (EBUSY, EPERM on Windows) gets one more try before it counts as failed.
func runSetup(dir, line string) bool {
	for attempt := 1; ; attempt++ {
		fmt.Printf("  setup   %s  (%s)\n", line, filepath.Base(dir))
		var seen bytes.Buffer
		cmd := shell(dir, line)
		cmd.Stdout = io.MultiWriter(os.Stderr, &seen)
		cmd.Stderr = cmd.Stdout
		err := cmd.Run()
		if err == nil {
			return true
		}
		busy := strings.Contains(seen.String(), "EBUSY") || strings.Contains(seen.String(), "EPERM")
		if attempt == 1 && busy {
			fmt.Println("  setup   files were busy: trying once more in a moment")
			time.Sleep(4 * time.Second)
			continue
		}
		fmt.Printf("  setup   failed: %v\n", err)
		return false
	}
}

// ensureSetup runs an app's setup lines once per lane, and says whether the
// app is set up. The main checkout and shared apps are the person's own.
func ensureSetup(lane Lane, cfg RepoConfig, app App) bool {
	if lane.IsMain || app.Shared || len(app.Setup) == 0 || setupDone(lane, app) {
		return true
	}
	release := acquireSetup(lane)
	defer release()
	// Another process may have finished it while this one waited.
	if setupDone(lane, app) {
		return true
	}

	dir := filepath.Join(lane.Path, filepath.FromSlash(app.Dir))
	for _, line := range app.Setup {
		if !runSetup(dir, cfg.expand(line, app, lane)) {
			fmt.Printf("%s is not installed in %s: `kitt setup %s` tries again\n", app.Name, lane.Name, lane.Name)
			return false
		}
	}
	_ = updateState(func(s *State) {
		if entry := s.Lanes[lane.key()]; entry != nil {
			entry.Setup = append(entry.Setup, app.Name)
		}
	})
	return true
}

func setupDone(lane Lane, app App) bool {
	if entry := loadState().Lanes[lane.key()]; entry != nil {
		for _, done := range entry.Setup {
			if done == app.Name {
				return true
			}
		}
	}
	return false
}

// missingSetup names the apps a lane runs that are not installed in it.
func missingSetup(lane Lane, cfg RepoConfig) []string {
	if lane.IsMain || !lane.Managed {
		return nil
	}
	var missing []string
	for _, app := range cfg.Apps {
		if lane.wants(app) && !app.Shared && len(app.Setup) > 0 {
			done := false
			if lane.State != nil {
				for _, name := range lane.State.Setup {
					done = done || name == app.Name
				}
			}
			if !done {
				missing = append(missing, app.Name)
			}
		}
	}
	return missing
}

// cmdSetup installs what a lane runs and is not installed yet.
func cmdSetup(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	var failed []string
	for _, app := range cfg.Apps {
		if lane.wants(app) && !ensureSetup(lane, cfg, app) {
			failed = append(failed, app.Name)
		}
	}
	if len(failed) > 0 {
		return fail("install failed for %s in %s", strings.Join(failed, ", "), lane.Name)
	}
	fmt.Printf("%s is installed\n", lane.Name)
	return nil
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
	rest, opts := flags(args, "force", "merged", "dry-run")
	if opts["merged"] != "" {
		return cmdRmMerged(opts["repo"], opts["dry-run"] != "")
	}
	if opts["dry-run"] != "" {
		return fail("--dry-run goes with --merged")
	}
	if len(rest) == 0 {
		return fail("usage: kitt rm <lane> [--force] | kitt rm --merged [--dry-run] [--repo r]")
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
	// A fork whose commits are all in its lane's branch has nothing a deleted branch would lose.
	return removeLane(lane, cfg, force, forkMerged(lane))
}

// removeLane takes a lane away: its servers, its leases, its links, its worktree
// and its state. `dropBranch` deletes the branch whether or not git sees it as
// merged (a squash-merged one never is); otherwise only a merged branch goes.
func removeLane(lane Lane, cfg RepoConfig, force, dropBranch bool) error {
	// Whatever the lane started (a container on its port) goes before its directory does.
	down(lane)
	// A lane that is gone holds nothing: its hard lease would keep other lanes out until its ttl.
	dropLease(lane)
	releaseEmulator(lane)

	// The linked files are symlinks into the main checkout: take the links away
	// first so no removal can follow one.
	for _, rel := range cfg.Link {
		link := filepath.Join(lane.Path, filepath.FromSlash(rel))
		if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
		}
	}

	if ws := herdrWorkspaceOf(lane.Main)[lane.key()]; ws != "" {
		_ = herdrNow().close(ws)
	}
	if err := removeWorktree(lane, force); err != nil {
		return err
	}
	_ = updateState(func(s *State) { delete(s.Lanes, lane.key()) })

	// -d only deletes what is merged; a squash-merged branch stays and says so.
	if lane.Branch != "" {
		flag := "-d"
		if dropBranch {
			flag = "-D"
		}
		if _, err := run(lane.Main, "git", "branch", flag, lane.Branch); err != nil {
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
	if err := deleteTree(lane.Path); err != nil {
		return err
	}
	_, _ = run(lane.Main, "git", "worktree", "prune")
	return nil
}

// deleteTree deletes a directory. A process that still has a folder of it as
// its working directory (a shell left open there) keeps Windows from removing
// that empty folder; nothing of the lane is in it any more, so that is said
// and not treated as a failure.
func deleteTree(path string) error {
	err := os.RemoveAll(path)
	if err == nil {
		return nil
	}
	files := 0
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() {
			files++
		}
		return nil
	})
	if files > 0 {
		return fail("could not delete %s (%d files left): %v", path, files, err)
	}
	fmt.Printf("an empty folder stays at %s: a process still has it open; it goes once that process ends\n", path)
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
		if err := deleteTree(entry.Path); err != nil {
			return err.Error()
		}
		_, _ = run(repo.Path, "git", "worktree", "prune")
		_ = updateState(func(s *State) { delete(s.Lanes, key) })
		branch := entry.Branch
		if branch == "" {
			branch = cfg.BranchPrefix + entry.Name
		}
		if _, err := run(repo.Path, "git", "branch", "-d", branch); err != nil {
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
	if forkMerged(lane) {
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

// cmdDetect prints what kitt would configure for a repo, without registering
// it or writing anything: a look before `kitt repo add` and `kitt init`.
func cmdDetect(args []string) error {
	rest, _ := flags(args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	top, err := run(dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return fail("%s is not a git repository", dir)
	}
	if common, err := run(top, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		top = filepath.Dir(common)
	}
	top = filepath.Clean(filepath.FromSlash(top))
	cfg := loadRepoConfig(top, "")
	fmt.Printf("# %s\n# source: %s\n\n%s", top, cfg.Source, renderConfig(cfg))
	return nil
}

// cmdPr checks an open pull request out as a lane, to look at it before the
// merge: its branch is fetched only now, into a worktree of its own, with the
// apps the PR touches as the lane's apps.
func cmdPr(args []string) error {
	rest, opts := flags(args, "focus", "no-setup")
	if len(rest) == 0 || !isNumber(rest[0]) {
		return fail("usage: kitt pr <number> [--repo r] [--focus]")
	}
	repo, err := findRepo(opts["repo"])
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(repo.Path, "")

	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Branch string `json:"headRefName"`
		Cross  bool   `json:"isCrossRepository"`
		Closes []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	out, err := run(repo.Path, "gh", "pr", "view", rest[0], "--json", "number,title,url,state,headRefName,isCrossRepository,closingIssuesReferences")
	if err != nil {
		return fail("PR #%s: %v", rest[0], err)
	}
	_ = json.Unmarshal([]byte(out), &pr)
	if pr.Cross {
		return fail("PR #%d comes from a fork: check it out with `gh pr checkout %d` in a worktree of your own, then `kitt adopt`", pr.Number, pr.Number)
	}

	// A branch that already has a lane is that lane.
	for _, lane := range lanesOf(repo, loadState()) {
		if lane.Branch == pr.Branch {
			if !lane.Managed {
				return fail("%s is checked out at %s, which is not a lane yet: kitt adopt %s", pr.Branch, lane.Path, lane.Path)
			}
			fmt.Printf("PR #%d is the lane %s\n", pr.Number, lane.Name)
			return nil
		}
	}

	fmt.Printf("fetching %s\n", pr.Branch)
	if _, err := runTimeout(repo.Path, 90*time.Second, "git", "fetch", "--quiet", "origin", pr.Branch+":refs/remotes/origin/"+pr.Branch); err != nil {
		return err
	}
	_, _ = runTimeout(repo.Path, 60*time.Second, "git", "fetch", "--quiet", "origin", cfg.Base)

	name := slug(pr.Branch[strings.LastIndex(pr.Branch, "/")+1:], 40)
	if name == "" {
		name = "pr-" + rest[0]
	}
	path := filepath.Join(cfg.lanesDir(repo.Path), name)
	if exists(path) {
		return fail("%s exists already", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if _, err := run(repo.Path, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+pr.Branch); err == nil {
		if _, err := runTimeout(repo.Path, 90*time.Second, "git", "worktree", "add", path, pr.Branch); err != nil {
			return err
		}
		// A local copy of the branch may be older than the PR: bring it up, or say that it differs.
		if _, err := run(path, "git", "merge", "--ff-only", "origin/"+pr.Branch); err != nil {
			fmt.Printf("  note    the local %s differs from the PR and was left as it is\n", pr.Branch)
		}
	} else if _, err := runTimeout(repo.Path, 90*time.Second, "git", "worktree", "add", "--track", "-b", pr.Branch, path, "origin/"+pr.Branch); err != nil {
		return err
	}

	entry := LaneState{Repo: repo.Name, Name: name, Path: path, Created: time.Now(), Title: pr.Title, URL: pr.URL}
	if len(pr.Closes) > 0 {
		entry.Issue = pr.Closes[0].Number
	}
	if err := updateState(func(s *State) {
		entry.Slot = s.freeSlot(repo.Name)
		s.Lanes[norm(path)] = &entry
	}); err != nil {
		return err
	}
	lane := Lane{Repo: repo.Name, Main: repo.Path, Path: path, Name: name, Branch: pr.Branch, Slot: entry.Slot, Managed: true, State: &entry}
	cfg = loadRepoConfig(repo.Path, path)

	// The lane is about what the PR touches.
	var apps []string
	for _, app := range touchedApps(lane, cfg) {
		if app.Dev != "" && !app.Shared {
			apps = append(apps, app.Name)
		}
	}
	if len(apps) > 0 {
		entry.Apps = apps
		_ = updateState(func(s *State) {
			if kept := s.Lanes[norm(path)]; kept != nil {
				kept.Apps = apps
			}
		})
	}

	fmt.Printf("lane %s  slot %d  PR #%d  %s\n  %s\n", name, lane.Slot, pr.Number, pr.Branch, path)
	if len(apps) > 0 {
		fmt.Printf("  apps    %s\n", strings.Join(apps, ", "))
	}
	linkFiles(lane, cfg, os.Stdout)
	if opts["no-setup"] == "" {
		setupLane(lane, cfg)
	}
	if hasHerdr() {
		if _, err := herdrOpen(lane, opts["focus"] != ""); err != nil {
			fmt.Printf("  herdr   %v\n", err)
		}
	}
	fmt.Printf("PR #%d is checked out as %s: enter opens it\n", pr.Number, name)
	return nil
}
