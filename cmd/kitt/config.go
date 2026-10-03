package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Global is kitt's own config: the repos it knows.
type Global struct {
	Repos []RepoRef `toml:"repo"`
}

type RepoRef struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
}

// RepoConfig is a repo's kitt.toml. Everything project-specific lives there, in
// the repo; what it leaves out is detected from the checkout.
type RepoConfig struct {
	Name         string   `toml:"name"`
	Base         string   `toml:"base"`
	Lanes        string   `toml:"lanes"`
	BranchPrefix string   `toml:"branch_prefix"`
	Link         []string `toml:"link"`
	Setup        []string `toml:"setup"`
	Agent        AgentCfg `toml:"agent"`
	Emulator     EmuCfg   `toml:"emulator"`
	Proof        ProofCfg `toml:"proof"`
	Apps         []App    `toml:"app"`

	// Source says where the config came from: a kitt.toml path, or "detected".
	Source string `toml:"-"`
}

type AgentCfg struct {
	Kind   string `toml:"kind"`
	Prompt string `toml:"prompt"`
}

type EmuCfg struct {
	Serial  string `toml:"serial"`
	Reverse []int  `toml:"reverse"`
}

type ProofCfg struct {
	Guide  string `toml:"guide"`
	WaitMs int    `toml:"wait_ms"`
}

// App is one runnable part of a repo. Kind is expo, web or backend.
type App struct {
	Name     string            `toml:"name"`
	Kind     string            `toml:"kind"`
	Dir      string            `toml:"dir"`
	Port     int               `toml:"port"`
	PortEnv  string            `toml:"port_env"`
	Shared   bool              `toml:"shared"`
	Dev      string            `toml:"dev"`
	Stop     string            `toml:"stop"`
	EnvFiles []string          `toml:"env_files"`
	Env      map[string]string `toml:"env"`
	Setup    []string          `toml:"setup"`
	Lazy     bool              `toml:"lazy"`
	Checks   []string          `toml:"checks"`
	Scheme   string            `toml:"scheme"`
	Package  string            `toml:"package"`
}

func configDir() string {
	if dir := os.Getenv("KITT_HOME"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "kitt")
}

func loadGlobal() Global {
	var g Global
	_, _ = toml.DecodeFile(filepath.Join(configDir(), "config.toml"), &g)
	return g
}

func saveGlobal(g Global) error {
	if err := os.MkdirAll(configDir(), 0o755); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(configDir(), "config.toml"))
	if err != nil {
		return err
	}
	defer file.Close()
	return toml.NewEncoder(file).Encode(g)
}

// loadRepoConfig reads kitt.toml from the lane, else from the main checkout,
// and fills whatever is missing from what the checkout itself shows.
func loadRepoConfig(mainRoot, laneRoot string) RepoConfig {
	var cfg RepoConfig
	cfg.Source = "detected"

	// The repo's own file wins; one kept in kitt's home stands in until the repo has it.
	var candidates []string
	for _, root := range []string{laneRoot, mainRoot} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "kitt.toml"))
		}
	}
	candidates = append(candidates, filepath.Join(configDir(), "repos", filepath.Base(mainRoot)+".toml"))
	for _, path := range candidates {
		if _, err := toml.DecodeFile(path, &cfg); err == nil {
			cfg.Source = path
			break
		}
	}

	if cfg.Name == "" {
		cfg.Name = filepath.Base(mainRoot)
	}
	if cfg.Base == "" {
		cfg.Base = detectBase(mainRoot)
	}
	if cfg.BranchPrefix == "" {
		cfg.BranchPrefix = "agent/"
	}
	if cfg.Lanes == "" {
		cfg.Lanes = "../" + filepath.Base(mainRoot) + "-lanes"
	}
	if cfg.Agent.Kind == "" {
		cfg.Agent.Kind = "claude"
	}
	if cfg.Proof.WaitMs == 0 {
		cfg.Proof.WaitMs = 8000
	}
	if cfg.Link == nil {
		cfg.Link = detectLinks(mainRoot)
	}
	if len(cfg.Apps) == 0 {
		cfg.Apps = detectApps(mainRoot)
	}
	for i := range cfg.Apps {
		if cfg.Apps[i].Kind == "expo" {
			fillExpo(mainRoot, &cfg.Apps[i])
		}
	}

	return cfg
}

func (c RepoConfig) lanesDir(mainRoot string) string {
	if filepath.IsAbs(c.Lanes) {
		return c.Lanes
	}
	return filepath.Clean(filepath.Join(mainRoot, c.Lanes))
}

func (c RepoConfig) app(name string) *App {
	for i := range c.Apps {
		if c.Apps[i].Name == name {
			return &c.Apps[i]
		}
	}
	return nil
}

func (c RepoConfig) expo() *App {
	for i := range c.Apps {
		if c.Apps[i].Kind == "expo" {
			return &c.Apps[i]
		}
	}
	return nil
}

// portStride is how far apart two lanes' ports are. Ten leaves room for apps
// whose base ports are neighbours (3000, 3001) without one lane's web landing
// on another lane's admin.
const portStride = 10

// port is where an app listens in a lane: its base port moved up by the lane's
// slot, or the base port itself for an app all lanes share.
func (a App) port(slot int) int {
	if a.Port == 0 {
		return 0
	}
	if a.Shared || slot < 0 {
		return a.Port
	}
	return a.Port + slot*portStride
}

var placeholder = regexp.MustCompile(`\{(port|lane|slot|root|main)(?::([a-zA-Z0-9_-]+))?\}`)

// expand fills {port}, {port:<app>}, {lane}, {slot}, {root} and {main}.
func (c RepoConfig) expand(text string, app App, lane Lane) string {
	return placeholder.ReplaceAllStringFunc(text, func(match string) string {
		parts := placeholder.FindStringSubmatch(match)
		switch parts[1] {
		case "port":
			target := app
			if parts[2] != "" {
				other := c.app(parts[2])
				if other == nil {
					return match
				}
				target = *other
			}
			return strconv.Itoa(target.port(lane.Slot))
		case "lane":
			return lane.Name
		case "slot":
			return strconv.Itoa(lane.Slot)
		case "root":
			return filepath.ToSlash(lane.Path)
		case "main":
			return filepath.ToSlash(lane.Main)
		}
		return match
	})
}

// --- Detection ---------------------------------------------------------------

func detectBase(root string) string {
	if head, err := run(root, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && head != "" {
		return strings.TrimPrefix(head, "origin/")
	}
	for _, name := range []string{"main", "master"} {
		if _, err := run(root, "git", "rev-parse", "--verify", "--quiet", "origin/"+name); err == nil {
			return name
		}
	}
	return "main"
}

var envFile = regexp.MustCompile(`^\.env(\..+)?$`)

// detectLinks picks the gitignored .env files of the main checkout: what a
// fresh worktree lacks and every app needs.
func detectLinks(root string) []string {
	out, err := run(root, "git", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil
	}
	var links []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "node_modules/") || strings.Contains(line, ".claude/worktrees/") {
			continue
		}
		if envFile.MatchString(filepath.Base(line)) {
			links = append(links, line)
		}
	}
	sort.Strings(links)
	return links
}

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, ".next": true, ".expo": true,
	"android": true, "ios": true, ".venv": true, "venv": true, "__pycache__": true, ".claude": true,
	"coverage": true, ".turbo": true,
}

// detectApps finds the runnable parts of a repo by their manifests: an Expo or
// web package.json, a Python backend.
func detectApps(root string) []App {
	var apps []App
	webPort := 3000

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		name := filepath.Base(dir)
		if rel == "." {
			name = filepath.Base(root)
		}

		if app, ok := detectNode(dir); ok {
			app.Name, app.Dir = name, rel
			if app.Kind == "web" {
				app.Port = webPort
				webPort++
			}
			apps = append(apps, app)
		} else if app, ok := detectPython(dir); ok {
			app.Name, app.Dir = name, rel
			apps = append(apps, app)
		}

		if depth >= 3 {
			return
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if entry.IsDir() && !skipDirs[entry.Name()] && !strings.HasPrefix(entry.Name(), ".") {
				walk(filepath.Join(dir, entry.Name()), depth+1)
			}
		}
	}
	walk(root, 0)

	return apps
}

func detectNode(dir string) (App, bool) {
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if readJSON(filepath.Join(dir, "package.json"), &pkg) != nil {
		return App{}, false
	}
	has := func(dep string) bool {
		_, a := pkg.Dependencies[dep]
		_, b := pkg.DevDependencies[dep]
		return a || b
	}

	pm, px := "npm", "npx"
	switch {
	case exists(filepath.Join(dir, "bun.lock")) || exists(filepath.Join(dir, "bun.lockb")):
		pm, px = "bun", "bunx"
	case exists(filepath.Join(dir, "pnpm-lock.yaml")):
		pm, px = "pnpm", "pnpm exec"
	case exists(filepath.Join(dir, "yarn.lock")):
		pm, px = "yarn", "yarn"
	}

	var app App
	switch {
	case has("expo"):
		app.Kind, app.Port = "expo", 8081
		app.Dev = px + " expo start --port {port}"
		if has("expo-dev-client") {
			app.Dev = px + " expo start --dev-client --port {port}"
		}
	case has("next"):
		app.Kind, app.Lazy = "web", true
		app.Dev = px + " next dev --port {port}"
	case has("vite"):
		app.Kind, app.Lazy = "web", true
		app.Dev = px + " vite --port {port}"
	default:
		return App{}, false
	}

	app.Setup = []string{pm + " install"}
	for _, script := range []string{"typecheck", "lint", "test"} {
		if _, ok := pkg.Scripts[script]; ok {
			app.Checks = append(app.Checks, pm+" run "+script)
		}
	}

	return app, true
}

func detectPython(dir string) (App, bool) {
	for _, manifest := range []string{"requirements.txt", "pyproject.toml"} {
		data, err := os.ReadFile(filepath.Join(dir, manifest))
		if err != nil {
			continue
		}
		text := strings.ToLower(string(data))
		if !strings.Contains(text, "fastapi") && !strings.Contains(text, "django") && !strings.Contains(text, "flask") {
			continue
		}
		app := App{Kind: "backend", Port: 8000, Shared: true}
		if exists(filepath.Join(dir, "tests")) {
			app.Checks = []string{"python -m pytest -q"}
		}
		return app, true
	}
	return App{}, false
}

// fillExpo reads the deep-link scheme and Android package from app.json.
func fillExpo(root string, app *App) {
	if app.Scheme != "" && app.Package != "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(root, app.Dir, "app.json"))
	if err != nil {
		return
	}
	var manifest struct {
		Expo struct {
			Slug    string `json:"slug"`
			Android struct {
				Package string `json:"package"`
			} `json:"android"`
		} `json:"expo"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return
	}
	if app.Scheme == "" && manifest.Expo.Slug != "" {
		app.Scheme = "exp+" + manifest.Expo.Slug
	}
	if app.Package == "" {
		app.Package = manifest.Expo.Android.Package
	}
}

// renderConfig writes a kitt.toml for `kitt init`: the detected values, spelled
// out so they can be edited.
func renderConfig(cfg RepoConfig) string {
	var b strings.Builder
	quote := func(items []string) string {
		quoted := make([]string, len(items))
		for i, item := range items {
			quoted[i] = strconv.Quote(item)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}

	fmt.Fprintf(&b, "name = %q\nbase = %q\nlanes = %q\nbranch_prefix = %q\n\n", cfg.Name, cfg.Base, cfg.Lanes, cfg.BranchPrefix)
	b.WriteString("# Gitignored files every lane gets as a symlink to the main checkout.\n")
	b.WriteString("link = [\n")
	for _, link := range cfg.Link {
		fmt.Fprintf(&b, "  %q,\n", link)
	}
	b.WriteString("]\n")

	for _, app := range cfg.Apps {
		fmt.Fprintf(&b, "\n[[app]]\nname = %q\nkind = %q\ndir = %q\nport = %d\n", app.Name, app.Kind, app.Dir, app.Port)
		if app.Shared {
			b.WriteString("shared = true\n")
		}
		if app.Dev != "" {
			fmt.Fprintf(&b, "dev = %q\n", app.Dev)
		}
		if len(app.Setup) > 0 {
			fmt.Fprintf(&b, "setup = %s\n", quote(app.Setup))
		}
		if app.Lazy {
			b.WriteString("lazy = true\n")
		}
		if len(app.Checks) > 0 {
			fmt.Fprintf(&b, "checks =%s\n", quote(app.Checks))
		}
	}

	return b.String()
}
