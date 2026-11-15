package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// `kitt init` is how a repo gets its kitt.toml: kitt detects what it can, an
// agent can read the repo for what detection cannot see, and a short wizard
// shows the result piece by piece for the person to keep, change or drop.

func cmdInit(args []string) error {
	rest, opts := flags(args, "force", "home", "agent", "yes")
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	in := bufio.NewReader(os.Stdin)
	interactive := opts["yes"] == ""

	top, err := repoTop(dir)
	if err != nil {
		return err
	}
	repo, registered := RepoRef{Path: top}, false
	for _, known := range loadGlobal().Repos {
		if norm(known.Path) == norm(top) {
			repo, registered = known, true
		}
	}

	cfg := loadRepoConfig(top, "")
	fmt.Printf("kitt init · %s\n  %s\n", cfg.Name, top)
	if cfg.Source != "detected" {
		fmt.Printf("  starting from %s\n", cfg.Source)
	} else {
		fmt.Printf("  detected %d apps, %d env files to link\n", len(cfg.Apps), len(cfg.Link))
	}

	if opts["agent"] != "" {
		fmt.Printf("\nAn agent is reading the repo for what detection cannot see (dev commands, checks, how the apps find each other).\nThis takes a minute or two…\n")
		draft, err := agentDraft(top, cfg)
		if err != nil {
			fmt.Printf("  the agent's draft could not be used: %v\n  continuing with what was detected\n", err)
		} else {
			draft.Source = "the agent's draft"
			cfg = fillDefaults(draft, top)
			fmt.Printf("  the agent proposes %d apps; review them below\n", len(cfg.Apps))
			for _, note := range cfg.Notes {
				fmt.Printf("  unsure: %s\n", note)
			}
		}
	}

	if interactive {
		cfg = wizard(in, cfg)
	}

	target := filepath.Join(top, "kitt.toml")
	if opts["home"] != "" {
		target = homeConfigPath(top)
	} else if interactive {
		fmt.Printf("\nWhere should it live?\n  1  %s  (in the repo: commit it, everyone gets it)\n  2  %s  (kitt's own folder: the repo stays untouched)\n",
			target, homeConfigPath(top))
		if ask(in, "Write to", "1") == "2" {
			target = homeConfigPath(top)
		}
	}
	if exists(target) && opts["force"] == "" {
		if !interactive || !yes(in, fmt.Sprintf("%s exists. Overwrite it", target), false) {
			return fail("%s exists: pass --force to overwrite it", target)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, []byte(renderConfig(cfg)), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", target)

	if !registered {
		if !interactive || yes(in, "Register this repo with kitt, so it shows in the dashboard", true) {
			g := loadGlobal()
			repo.Name = cfg.Name
			g.Repos = append(g.Repos, repo)
			if err := saveGlobal(g); err != nil {
				return err
			}
			fmt.Printf("registered %s\n", cfg.Name)
		}
	}
	fmt.Println("next: `kitt dash`, then n for a lane. `kitt init` again reviews the file any time.")
	return nil
}

func homeConfigPath(top string) string {
	return filepath.Join(configDir(), "repos", filepath.Base(top)+".toml")
}

// repoTop is the main checkout of the repo a directory belongs to.
func repoTop(dir string) (string, error) {
	top, err := run(dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fail("%s is not a git repository", dir)
	}
	if common, err := run(top, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		top = filepath.Dir(common)
	}
	return filepath.Clean(filepath.FromSlash(top)), nil
}

// fillDefaults gives a config from elsewhere (an agent's draft) what
// loadRepoConfig gives one from a file.
func fillDefaults(cfg RepoConfig, top string) RepoConfig {
	if cfg.Name == "" {
		cfg.Name = filepath.Base(top)
	}
	if cfg.Base == "" {
		cfg.Base = detectBase(top)
	}
	if cfg.BranchPrefix == "" {
		cfg.BranchPrefix = "feat/"
	}
	if cfg.Lanes == "" {
		cfg.Lanes = "../" + filepath.Base(top) + "-lanes"
	}
	if cfg.Link == nil {
		cfg.Link = detectLinks(top)
	}
	for i := range cfg.Apps {
		if cfg.Apps[i].Kind == "expo" {
			fillExpo(top, &cfg.Apps[i])
		}
	}
	return cfg
}

// --- The wizard ----------------------------------------------------------------

func ask(in *bufio.Reader, question, current string) string {
	if current == "" {
		fmt.Printf("%s: ", question)
	} else {
		fmt.Printf("%s [%s]: ", question, current)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return current
	}
	if line == "-" {
		return ""
	}
	return line
}

func yes(in *bufio.Reader, question string, fallback bool) bool {
	hint := "y/N"
	if fallback {
		hint = "Y/n"
	}
	fmt.Printf("%s? [%s]: ", question, hint)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "j", "ja":
		return true
	case "n", "no", "nein":
		return false
	}
	return fallback
}

func list(text string, separator string) []string {
	var items []string
	for _, item := range strings.Split(text, separator) {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func showApp(number int, app App) {
	flags := ""
	if app.Shared {
		flags += " · shared by all lanes"
	}
	if app.Lazy {
		flags += " · starts only when named"
	}
	port := "no port"
	if app.Port > 0 {
		port = fmt.Sprintf("port %d", app.Port)
	}
	fmt.Printf("\n  %d. %s  %s  %s  %s%s\n", number, app.Name, app.Kind, app.Dir, port, flags)
	show := func(label, value string) {
		if value == "" {
			value = "(none)"
		}
		fmt.Printf("     %-7s %s\n", label, value)
	}
	show("dev", app.Dev)
	show("checks", strings.Join(app.Checks, "  ;  "))
	if len(app.EnvFiles) > 0 {
		show("env", strings.Join(app.EnvFiles, ", "))
	}
	for _, key := range sortedKeys(app.Env) {
		show("sets", key+" = "+app.Env[key])
	}
}

func editApp(in *bufio.Reader, app App) App {
	fmt.Println("     Enter keeps a value, - empties it.")
	app.Name = ask(in, "     name", app.Name)
	app.Kind = ask(in, "     kind (expo, web, backend)", app.Kind)
	app.Dir = ask(in, "     directory", app.Dir)
	if port, err := strconv.Atoi(ask(in, "     base port (0 for none)", strconv.Itoa(app.Port))); err == nil {
		app.Port = port
	}
	app.Dev = ask(in, "     dev command ({port} is the lane's port)", app.Dev)
	app.Checks = list(ask(in, "     checks, separated by ;", strings.Join(app.Checks, " ; ")), ";")
	app.Setup = list(ask(in, "     setup in a fresh lane, separated by ;", strings.Join(app.Setup, " ; ")), ";")
	app.EnvFiles = list(ask(in, "     env files loaded for dev, separated by ,", strings.Join(app.EnvFiles, ", ")), ",")
	app.Shared = yes(in, "     one instance for all lanes (a database, a backend with a fixed port)", app.Shared)
	app.Lazy = yes(in, "     start only when named", app.Lazy)
	return app
}

func wizard(in *bufio.Reader, cfg RepoConfig) RepoConfig {
	fmt.Println("\nEnter keeps what is shown.")
	cfg.Name = ask(in, "Name in the dashboard", cfg.Name)
	cfg.Base = ask(in, "Base branch", cfg.Base)
	cfg.BranchPrefix = ask(in, "Lane branches start with", cfg.BranchPrefix)
	cfg.Lanes = ask(in, "Folder for the lanes' worktrees", cfg.Lanes)

	fmt.Printf("\nApps (%d). For each: Enter keeps it, e edits it, d drops it.\n", len(cfg.Apps))
	var kept []App
	for i, app := range cfg.Apps {
		showApp(i+1, app)
		switch strings.ToLower(ask(in, "     keep / e / d", "keep")) {
		case "d", "drop":
			continue
		case "e", "edit":
			app = editApp(in, app)
			showApp(i+1, app)
		}
		kept = append(kept, app)
	}
	for yes(in, "\nAdd an app kitt did not find", false) {
		app := editApp(in, App{Kind: "web"})
		if app.Name != "" {
			kept = append(kept, app)
		}
	}
	cfg.Apps = kept

	// Two apps on one base port would meet in every lane.
	seen := map[int]string{}
	for _, app := range cfg.Apps {
		if app.Port == 0 {
			continue
		}
		if other, taken := seen[app.Port]; taken {
			fmt.Printf("\n  ! %s and %s both use port %d: edit one of them in the file\n", other, app.Name, app.Port)
		}
		seen[app.Port] = app.Name
	}

	fmt.Printf("\nGitignored files every lane gets as a symlink to this checkout (%d):\n", len(cfg.Link))
	for _, link := range cfg.Link {
		fmt.Printf("  %s\n", link)
	}
	if answer := ask(in, "Enter keeps them, or give the full list separated by ,", ""); answer != "" {
		cfg.Link = list(answer, ",")
	}

	if cfg.expo() != nil {
		ports := make([]string, len(cfg.Emulator.Reverse))
		for i, port := range cfg.Emulator.Reverse {
			ports[i] = strconv.Itoa(port)
		}
		fmt.Println("\nThe emulator reaches each app's own port. Does the phone app talk to anything else on localhost (a local database)?")
		cfg.Emulator.Reverse = nil
		for _, item := range list(ask(in, "Extra ports, separated by ,", strings.Join(ports, ", ")), ",") {
			if port, err := strconv.Atoi(item); err == nil {
				cfg.Emulator.Reverse = append(cfg.Emulator.Reverse, port)
			}
		}
	}

	fmt.Println("\nHow does an agent prove a change here? One line, or the path of a file in the repo (test accounts, how to reach a screen).")
	cfg.Proof.Guide = ask(in, "Proof guide", cfg.Proof.Guide)

	return cfg
}

// --- The agent's draft -----------------------------------------------------------

const draftPrompt = `You are setting up kitt for this repository. kitt runs parallel work as lanes: one git worktree per ticket, each app on its own port per lane.

Below is the kitt.toml that kitt detected from manifests alone. Improve it by reading the repo: justfile, Makefile, package.json scripts (root and apps), docker-compose files, CI workflows under .github/workflows, AGENTS.md, CLAUDE.md, README, docs. Only read; change nothing.

What to settle, per app:
- dev: the command that starts its dev server. It must take the lane's port: write {port} where the port goes. If the port cannot be set, mark the app shared = true (one instance for all lanes) and say why in a comment.
- checks: exactly what CI runs for that app (lint, type check, tests), as commands run from the app's directory. {root} is the lane's root.
- setup: how a fresh checkout installs its dependencies, with a frozen lockfile where the tool has one.
- env_files: env files in the app's directory that select the LOCAL environment and must be loaded for dev, if the repo has that convention.
- [app.env]: variables that tell this app where another app of the same lane listens, written with {port:<app name>}.
- port_env: a variable the app reads its port from, if any.
- env_out: for an app whose bundler inlines env files into the client and lets them win over the process environment (Expo's EXPO_PUBLIC_*), a gitignored, not-linked file in the app's directory that kitt writes [app.env] and port_env into on every start, e.g. ".env.development.local". Only when the repo's .gitignore covers it.
- stop: a command that ends what dev started, when a container outlives its terminal.
- lazy = true for apps that are not needed for most tickets.
Also: drop detected apps that are not runnable apps, add apps detection missed (a database, a worker), and never give two apps the same base port.
For the repo: base, link (gitignored env files to symlink into each lane), [emulator] reverse (ports the phone app reaches on localhost besides the apps' own), [proof] guide (one or two lines: test accounts, local database, what to show), [stack] mode = "single" when the machine cannot run several lanes' servers at once (fixed ports, heavy services), with keep = [apps never stopped].

Rules: keep every key name as in the detected file. Do not invent commands: every command must come from a file you read. Where you are unsure, keep the detected value and add a TOML comment starting with "# unsure:".

Answer with the complete kitt.toml in one fenced toml block and nothing after it.

Detected:
`

var fenced = regexp.MustCompile("(?s)```(?:toml)?\\s*\\n(.*?)```")

// agentDraft has a headless agent read the repo and return a fuller config.
func agentDraft(top string, cfg RepoConfig) (RepoConfig, error) {
	prompt := draftPrompt + "```toml\n" + renderConfig(cfg) + "```\n"
	out, err := runTimeout(top, 8*time.Minute, "claude", "-p", prompt, "--allowedTools", "Read,Grep,Glob", "--output-format", "text")
	if err != nil {
		return RepoConfig{}, err
	}
	blocks := fenced.FindAllStringSubmatch(out, -1)
	if len(blocks) == 0 {
		return RepoConfig{}, fail("the agent answered without a toml block")
	}
	var draft RepoConfig
	if _, err := toml.Decode(blocks[len(blocks)-1][1], &draft); err != nil {
		return RepoConfig{}, fail("the agent's toml does not parse: %v", err)
	}
	if len(draft.Apps) == 0 {
		return RepoConfig{}, fail("the agent's draft has no apps")
	}
	// Decoding drops comments; what the agent flagged must reach the person.
	for _, line := range strings.Split(blocks[len(blocks)-1][1], "\n") {
		if _, note, ok := strings.Cut(line, "# unsure:"); ok {
			draft.Notes = append(draft.Notes, strings.TrimSpace(note))
		}
	}
	return draft, nil
}

// --- Writing the file ------------------------------------------------------------

// renderConfig writes a config as the kitt.toml a person can read and edit.
func renderConfig(cfg RepoConfig) string {
	var b strings.Builder
	quote := func(items []string) string {
		quoted := make([]string, len(items))
		for i, item := range items {
			quoted[i] = tomlString(item)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}

	fmt.Fprintf(&b, "name = %s\nbase = %s\nlanes = %s\nbranch_prefix = %s\n\n", tomlString(cfg.Name), tomlString(cfg.Base), tomlString(cfg.Lanes), tomlString(cfg.BranchPrefix))
	b.WriteString("# Gitignored files every lane gets as a symlink to the main checkout.\nlink = [\n")
	for _, link := range cfg.Link {
		fmt.Fprintf(&b, "  %s,\n", tomlString(link))
	}
	b.WriteString("]\n")

	if len(cfg.Emulator.Reverse) > 0 || cfg.Emulator.Serial != "" || cfg.Emulator.Platform != "" || cfg.Emulator.Simulator != "" {
		b.WriteString("\n[emulator]\n")
		if cfg.Emulator.Platform != "" {
			fmt.Fprintf(&b, "platform = %s\n", tomlString(cfg.Emulator.Platform))
		}
		if cfg.Emulator.Simulator != "" {
			fmt.Fprintf(&b, "simulator = %s\n", tomlString(cfg.Emulator.Simulator))
		}
		if cfg.Emulator.Serial != "" {
			fmt.Fprintf(&b, "serial = %s\n", tomlString(cfg.Emulator.Serial))
		}
		if len(cfg.Emulator.Reverse) > 0 {
			ports := make([]string, len(cfg.Emulator.Reverse))
			for i, port := range cfg.Emulator.Reverse {
				ports[i] = strconv.Itoa(port)
			}
			fmt.Fprintf(&b, "# Ports besides the apps' own that the emulator must reach on localhost.\nreverse = [%s]\n", strings.Join(ports, ", "))
		}
	}
	if cfg.Stack.Mode != "" || len(cfg.Stack.Keep) > 0 || cfg.Stack.LeaseTTL != "" {
		b.WriteString("\n[stack]\n")
		if cfg.Stack.Mode != "" {
			fmt.Fprintf(&b, "mode = %s\n", tomlString(cfg.Stack.Mode))
		}
		if cfg.Stack.LeaseTTL != "" {
			fmt.Fprintf(&b, "lease_ttl = %s\n", tomlString(cfg.Stack.LeaseTTL))
		}
		if len(cfg.Stack.Keep) > 0 {
			fmt.Fprintf(&b, "keep = %s\n", quote(cfg.Stack.Keep))
		}
	}
	if cfg.Proof.Guide != "" {
		fmt.Fprintf(&b, "\n[proof]\nguide = %s\n", tomlString(cfg.Proof.Guide))
	}
	if cfg.Agent.Prompt != "" {
		fmt.Fprintf(&b, "\n[agent]\nprompt = %s\n", tomlString(cfg.Agent.Prompt))
	}

	for _, app := range cfg.Apps {
		fmt.Fprintf(&b, "\n[[app]]\nname = %s\nkind = %s\ndir = %s\nport = %d\n", tomlString(app.Name), tomlString(app.Kind), tomlString(app.Dir), app.Port)
		if app.PortEnv != "" {
			fmt.Fprintf(&b, "port_env = %s\n", tomlString(app.PortEnv))
		}
		if app.Shared {
			b.WriteString("shared = true\n")
		}
		if app.Lazy {
			b.WriteString("lazy = true\n")
		}
		if app.Dev != "" {
			fmt.Fprintf(&b, "dev = %s\n", tomlString(app.Dev))
		}
		if app.Stop != "" {
			fmt.Fprintf(&b, "stop = %s\n", tomlString(app.Stop))
		}
		if len(app.EnvFiles) > 0 {
			fmt.Fprintf(&b, "env_files = %s\n", quote(app.EnvFiles))
		}
		if app.EnvOut != "" {
			fmt.Fprintf(&b, "env_out = %s\n", tomlString(app.EnvOut))
		}
		if len(app.Setup) > 0 {
			fmt.Fprintf(&b, "setup = %s\n", quote(app.Setup))
		}
		if len(app.Checks) > 0 {
			fmt.Fprintf(&b, "checks = %s\n", quote(app.Checks))
		}
		if len(app.Env) > 0 {
			b.WriteString("\n[app.env]\n")
			keys := sortedKeys(app.Env)
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&b, "%s = %s\n", key, tomlString(app.Env[key]))
			}
		}
	}

	if len(cfg.Notes) > 0 {
		b.WriteString("\n# The agent that drafted this was unsure about:\n")
		for _, note := range cfg.Notes {
			fmt.Fprintf(&b, "#   %s\n", note)
		}
	}

	b.WriteString(`
# Placeholders in dev, stop, setup, checks and env values:
#   {port} this app's port in the lane · {port:<app>} another app's · {lane} · {slot} · {root} the lane · {main} the main checkout
# Keys an app can have: port_env, shared, lazy, dev, stop, env_files, env_out, setup, checks, and [app.env].
# env_out = ".env.development.local" writes [app.env] and port_env into that (gitignored) file on every up,
# for bundlers that inline env files over the process env (Expo).
# [stack] mode = "single" runs one lane's servers at a time (keep = ["db"] never stops those apps).
`)

	return b.String()
}

// tomlString writes a string so that quotes and backslashes inside it survive:
// a literal string where it can, a basic one otherwise.
func tomlString(s string) string {
	if strings.ContainsAny(s, "\"\\") && !strings.ContainsAny(s, "'\n") {
		return "'" + s + "'"
	}
	if strings.Contains(s, "\n") {
		return "\"\"\"\n" + strings.ReplaceAll(s, `\`, `\\`) + "\"\"\""
	}
	return strconv.Quote(s)
}
