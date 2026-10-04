package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// devTab is the one herdr tab of a lane's workspace its dev servers run in,
// side by side: what is running for this ticket is one glance.
const devTab = "dev"

// appEnv is the environment an app's dev server starts with in a lane: its env
// files, then the config's own values, then its port.
func appEnv(cfg RepoConfig, app App, lane Lane, dir string) map[string]string {
	env := map[string]string{}
	for _, name := range app.EnvFiles {
		file, err := os.Open(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			key, value, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(line, "#") {
				continue
			}
			env[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		file.Close()
	}
	for key, value := range app.Env {
		env[key] = cfg.expand(value, app, lane)
	}
	if app.PortEnv != "" {
		env[app.PortEnv] = strconv.Itoa(app.port(lane.Slot))
	}
	return env
}

func appDir(app App, lane Lane) string {
	root := lane.Path
	if app.Shared {
		root = lane.Main
	}
	return filepath.Join(root, filepath.FromSlash(app.Dir))
}

// cmdUp starts a lane's dev servers in the lane's dev tab, one pane each. An
// app that already answers on its port is left alone.
func cmdUp(args []string) error {
	rest, opts := flags(args, "print")
	laneArg, only := "", []string{}
	if len(rest) > 0 {
		laneArg, only = rest[0], rest[1:]
	}
	lane, err := findLane(laneArg)
	if err != nil {
		// `kitt up mobile` inside a lane: the first word was an app, not a lane.
		if lane, err = findLane(""); err != nil {
			return err
		}
		only = rest
	}
	started, err := up(lane, only, opts["print"] != "")
	for _, line := range started {
		fmt.Println(line)
	}
	return err
}

func up(lane Lane, only []string, printOnly bool) ([]string, error) {
	if !lane.Managed {
		return nil, fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	// Unnamed, `up` starts the apps the lane is about; any other starts when named.
	touched := map[string]bool{}
	if len(only) == 0 && !lane.IsMain {
		for _, app := range touchedApps(lane, cfg) {
			touched[app.Name] = true
		}
	}
	wanted := func(app App) bool {
		if len(only) == 0 {
			// What the lane is about, and whatever it has changed files of since.
			return lane.wants(app) || touched[app.Name]
		}
		for _, name := range only {
			if name == app.Name {
				return true
			}
		}
		return false
	}

	var lines []string
	workspace := ""
	for _, app := range cfg.Apps {
		if app.Dev == "" || !wanted(app) {
			continue
		}
		port := app.port(lane.Slot)
		if listening(port) {
			lines = append(lines, fmt.Sprintf("%-8s already up on %d", app.Name, port))
			continue
		}
		dir := appDir(app, lane)
		command := cfg.expand(app.Dev, app, lane)
		env := appEnv(cfg, app, lane, dir)

		if printOnly || !hasHerdr() {
			lines = append(lines, fmt.Sprintf("%-8s cd %s && %s%s", app.Name, dir, envPrefix(env), command))
			continue
		}
		ensureSetup(lane, cfg, app)
		if workspace == "" {
			if workspace, _ = herdrOpen(lane, false); workspace == "" {
				return lines, fail("could not open %s in herdr", lane.Name)
			}
		}

		pane, err := devPane(workspace, dir, env)
		if err != nil {
			return lines, err
		}
		_ = herdr(nil, "pane", "rename", pane, fmt.Sprintf("%s :%d", app.Name, port))
		if err := herdr(nil, "pane", "run", pane, command); err != nil {
			return lines, err
		}
		lines = append(lines, fmt.Sprintf("%-8s starting on %d", app.Name, port))
	}
	if len(lines) == 0 {
		lines = append(lines, "nothing to start: no app of this repo has a `dev` command")
	}
	return lines, nil
}

// devPane makes the pane an app's dev server runs in: the dev tab's first pane,
// or a split to the right of its last one. The environment is the pane's own,
// set as it is created, so no secret is typed into a shell.
func devPane(workspace, dir string, env map[string]string) (string, error) {
	var envArgs []string
	for _, key := range sortedKeys(env) {
		envArgs = append(envArgs, "--env", key+"="+env[key])
	}

	tab := ""
	for _, candidate := range herdrTabs(workspace) {
		if candidate.Label == devTab {
			tab = candidate.TabID
		}
	}
	if tab == "" {
		var created struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		}
		args := append([]string{"tab", "create", "--workspace", workspace, "--cwd", dir, "--label", devTab, "--no-focus"}, envArgs...)
		if err := herdr(&created, args...); err != nil {
			return "", err
		}
		return created.RootPane.ID, nil
	}

	last := ""
	for _, pane := range herdrPanes(workspace) {
		if pane.TabID == tab {
			last = pane.PaneID
		}
	}
	if last == "" {
		return "", fail("the dev tab of workspace %s has no pane", workspace)
	}
	var split struct {
		Pane struct {
			ID string `json:"pane_id"`
		} `json:"pane"`
	}
	args := append([]string{"pane", "split", last, "--direction", "right", "--cwd", dir, "--no-focus"}, envArgs...)
	if err := herdr(&split, args...); err != nil {
		return "", err
	}
	return split.Pane.ID, nil
}

// cmdOpen opens a lane's web app in the browser.
func cmdOpen(args []string) error {
	rest, _ := flags(args)
	laneArg := ""
	if len(rest) > 0 {
		laneArg = rest[0]
	}
	lane, err := findLane(laneArg)
	if err != nil {
		return err
	}
	message, err := openWeb(lane, rest[min(1, len(rest)):])
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

func openWeb(lane Lane, only []string) (string, error) {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	var down []string
	for _, app := range cfg.Apps {
		if app.Kind != "web" || (len(only) > 0 && only[0] != app.Name) {
			continue
		}
		port := app.port(lane.Slot)
		if !listening(port) {
			down = append(down, fmt.Sprintf("%s (%d)", app.Name, port))
			continue
		}
		url := fmt.Sprintf("http://localhost:%d", port)
		return "browser → " + lane.Name + " " + app.Name + " " + url, openFile(url)
	}
	if len(down) > 0 {
		return "", fail("no web app of %s is running: %s. Start one with kitt up %s <app>", lane.Name, strings.Join(down, ", "), lane.Name)
	}
	return "", fail("%s has no web app", lane.Repo)
}

// cmdDown stops a lane's dev servers: each app's own stop line, then the dev tab.
func cmdDown(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	for _, line := range down(lane) {
		fmt.Println(line)
	}
	return nil
}

func down(lane Lane) []string {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	var lines []string

	closed := 0
	if workspace := herdrWorkspaceOf(lane.Main)[lane.key()]; workspace != "" {
		for _, tab := range herdrTabs(workspace) {
			if tab.Label == devTab || strings.HasPrefix(tab.Label, "dev:") {
				if herdr(nil, "tab", "close", tab.TabID) == nil {
					closed++
				}
			}
		}
	}

	// Closing a pane ends what runs in it, but not what that started elsewhere
	// (a container): the app's stop line takes that down.
	for _, app := range cfg.Apps {
		if app.Stop == "" || app.Shared {
			continue
		}
		line := cfg.expand(app.Stop, app, lane)
		cmd := shell(appDir(app, lane), line)
		if out, err := cmd.CombinedOutput(); err != nil {
			lines = append(lines, fmt.Sprintf("%-8s stop failed: %s", app.Name, firstLine(strings.TrimSpace(string(out)))))
		} else {
			lines = append(lines, fmt.Sprintf("%-8s stopped", app.Name))
		}
	}
	lines = append(lines, fmt.Sprintf("closed %d dev tabs of %s", closed, lane.Name))
	return lines
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func envPrefix(env map[string]string) string {
	var b strings.Builder
	for _, key := range sortedKeys(env) {
		// Values of env files are secrets as often as not: name them, do not print them.
		fmt.Fprintf(&b, "%s=… ", key)
	}
	return b.String()
}
