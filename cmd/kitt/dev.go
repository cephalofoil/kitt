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

// cmdUp starts a lane's dev servers, each in its own herdr tab of the lane's
// workspace. An app that already answers on its port is left alone.
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
	// Unnamed, `up` starts what every lane needs; a lazy app starts when named.
	wanted := func(app App) bool {
		if len(only) == 0 {
			return !app.Lazy
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
		if !printOnly {
			ensureSetup(lane, cfg, app)
		}
		env := appEnv(cfg, app, lane, dir)

		if printOnly || !hasHerdr() {
			lines = append(lines, fmt.Sprintf("%-8s cd %s && %s%s", app.Name, dir, envPrefix(env), command))
			continue
		}
		if workspace == "" {
			if workspace, _ = herdrOpen(lane, false); workspace == "" {
				return lines, fail("could not open %s in herdr", lane.Name)
			}
		}

		var created struct {
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		}
		args := []string{"tab", "create", "--workspace", workspace, "--cwd", dir, "--label", "dev:" + app.Name, "--no-focus"}
		for _, key := range sortedKeys(env) {
			args = append(args, "--env", key+"="+env[key])
		}
		if err := herdr(&created, args...); err != nil {
			return lines, err
		}
		if err := herdr(nil, "pane", "run", created.RootPane.ID, command); err != nil {
			return lines, err
		}
		lines = append(lines, fmt.Sprintf("%-8s starting on %d  (tab dev:%s)", app.Name, port, app.Name))
	}
	if len(lines) == 0 {
		lines = append(lines, "nothing to start: no app of this repo has a `dev` command")
	}
	return lines, nil
}

// cmdDown closes the dev tabs kitt opened in a lane's workspace.
func cmdDown(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	workspace := herdrWorkspaceOf(lane.Main)[lane.key()]
	if workspace == "" {
		return fail("%s is not open in herdr", lane.Name)
	}
	closed := 0
	for _, tab := range herdrTabs(workspace) {
		if strings.HasPrefix(tab.Label, "dev:") {
			if herdr(nil, "tab", "close", tab.TabID) == nil {
				closed++
			}
		}
	}
	fmt.Printf("closed %d dev tabs of %s\n", closed, lane.Name)
	return nil
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
