package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	prwatch "github.com/cephalofoil/kitt/opencode/pr-watch"
)

// What a lane's agent is. Both herdrs start any of herdr's agent kinds; kitt
// knows these two by name, and brings opencode what it lacks in a lane.
const (
	kindClaude   = "claude"
	kindOpencode = "opencode"
)

// agentKinds are the kinds the dashboards offer, in their order.
var agentKinds = []string{kindClaude, kindOpencode}

//go:embed opencode/kitt-status.js
var kittStatusJS []byte

// laneAgentKind is the kind a lane's agent starts as: the one asked for
// (--with), else the one the lane last ran, else the repo's [agent] kind.
func laneAgentKind(lane Lane, cfg RepoConfig, with string) string {
	switch {
	case with != "":
		return with
	case lane.State != nil && lane.State.AgentKind != "":
		return lane.State.AgentKind
	}
	return cfg.Agent.Kind
}

// withAgentKind settles the kind for this command and, when it was asked for,
// keeps it with the lane, so the next agent of the lane is of that kind too.
func withAgentKind(lane Lane, cfg *RepoConfig, with string) error {
	if with != "" {
		if err := checkAgentKind(with); err != nil {
			return err
		}
		if lane.Managed {
			if err := updateState(func(s *State) { s.laneEntry(lane).AgentKind = with }); err != nil {
				return err
			}
		}
	}
	cfg.Agent.Kind = laneAgentKind(lane, *cfg, with)
	return nil
}

func checkAgentKind(kind string) error {
	if strings.ContainsAny(kind, " /\\") {
		return fail("--with %q: name an agent, such as claude or opencode", kind)
	}
	if _, err := exec.LookPath(kind); err != nil {
		return fail("--with %s: %s is not installed (not on PATH)", kind, kind)
	}
	return nil
}

// agentLabel is how the dashboards name an agent of a kind.
func agentLabel(kind string) string {
	switch kind {
	case "", kindClaude:
		return "Claude"
	case kindOpencode:
		return "opencode"
	}
	return kind
}

// agentEnv is what an agent of a kind needs in its environment beyond the
// lane's shell: opencode gets kitt's opencode folder, with pr-watch and the
// status the herdr app shows, and nothing is written into the worktree.
func agentEnv(kind string) (map[string]string, error) {
	if kind != kindOpencode {
		return nil, nil
	}
	dir := filepath.Join(configDir(), "opencode")
	if err := writeOpencodePlugins(dir, "."); err != nil {
		return nil, err
	}
	if err := writeJSONFile(filepath.Join(dir, "opencode.json"), map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"plugin":  []string{"./pr-watch/server.ts", "./kitt-status.js"},
	}); err != nil {
		return nil, err
	}
	if err := writeJSONFile(filepath.Join(dir, "tui.json"), map[string]any{
		"$schema": "https://opencode.ai/tui.json",
		"plugin":  []any{[]any{"./pr-watch/tui.tsx", map[string]any{"autoRebase": false}}},
	}); err != nil {
		return nil, err
	}
	return map[string]string{"OPENCODE_CONFIG_DIR": dir}, nil
}

// writeOpencodePlugins writes pr-watch and the status plugin into dir/sub.
// Neither goes into a plugins/ folder: opencode loads every file there as a
// plugin of its own, pr-watch's helpers included.
func writeOpencodePlugins(dir, sub string) error {
	base := filepath.Join(dir, sub)
	err := fs.WalkDir(prwatch.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := prwatch.Files.ReadFile(path)
		if err != nil {
			return err
		}
		return writeIfChanged(filepath.Join(base, "pr-watch", filepath.FromSlash(path)), data)
	})
	if err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(base, "kitt-status.js"), kittStatusJS)
}

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeIfChanged(path, append(data, '\n'))
}

// --- kitt opencode -----------------------------------------------------------

// The global install: kitt's plugins in a folder of their own inside opencode's
// config, and listed in its opencode.json and tui.json beside what is there.
const (
	opencodeSub    = "kitt"
	opencodeServer = "./kitt/pr-watch/server.ts"
	opencodeStatus = "./kitt/kitt-status.js"
	opencodeTUI    = "./kitt/pr-watch/tui.tsx"
)

func opencodeHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "opencode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "opencode")
}

// cmdOpencode installs kitt's opencode plugins for every opencode session,
// removes them, or says where they are. In lanes kitt starts opencode with
// them without an install.
func cmdOpencode(args []string) error {
	home := opencodeHome()
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "":
		on := opencodeListed(filepath.Join(home, "opencode.json"), opencodeServer)
		fmt.Printf("in lanes   kitt starts opencode with pr-watch and the herdr app's status (%s)\n", filepath.Join(configDir(), "opencode"))
		if on {
			fmt.Printf("everywhere installed in %s (kitt opencode uninstall)\n", home)
		} else {
			fmt.Printf("everywhere not installed (kitt opencode install puts them into %s)\n", home)
		}
		if hasTUIIntegration() == "missing" {
			fmt.Println("herdr TUI  its opencode status needs: herdr integration install opencode")
		}
		return nil
	case "install":
		if err := writeOpencodePlugins(home, opencodeSub); err != nil {
			return err
		}
		if err := editOpencodeList(filepath.Join(home, "opencode.json"), "https://opencode.ai/config.json", true, opencodeServer, opencodeStatus); err != nil {
			return err
		}
		if err := editOpencodeList(filepath.Join(home, "tui.json"), "https://opencode.ai/tui.json", true, opencodeTUI); err != nil {
			return err
		}
		fmt.Printf("installed pr-watch and the herdr app's status into %s: a new opencode session has them\n", filepath.Join(home, opencodeSub))
		return nil
	case "uninstall":
		if err := editOpencodeList(filepath.Join(home, "opencode.json"), "", false, opencodeServer, opencodeStatus); err != nil {
			return err
		}
		if err := editOpencodeList(filepath.Join(home, "tui.json"), "", false, opencodeTUI); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(home, opencodeSub)); err != nil {
			return err
		}
		fmt.Printf("removed kitt's opencode plugins from %s\n", home)
		return nil
	}
	return fail("usage: kitt opencode [install | uninstall]")
}

// editOpencodeList adds specs to (or removes them from) the plugin list of an
// opencode config file, keeping everything else in it. A file kitt cannot read
// as JSON (comments, a .jsonc) is left alone, and the error says what to add.
func editOpencodeList(path, schema string, add bool, specs ...string) error {
	config := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if !add {
			return nil
		}
		raw, _ := json.Marshal(schema)
		config["$schema"] = raw
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &config); err != nil {
			return fail("%s is not plain JSON (%v): add %s to its \"plugin\" list by hand", path, err, strings.Join(specs, ", "))
		}
	}

	var list []any
	if raw, ok := config["plugin"]; ok {
		if err := json.Unmarshal(raw, &list); err != nil {
			return fail("%s: its \"plugin\" is not a list (%v)", path, err)
		}
	}
	var kept []any
	for _, entry := range list {
		if !isSpec(entry, specs) {
			kept = append(kept, entry)
		}
	}
	if add {
		for _, spec := range specs {
			kept = append(kept, spec)
		}
	}
	before, _ := json.Marshal(list)
	after, _ := json.Marshal(kept)
	if data != nil && string(before) == string(after) {
		return nil
	}
	if len(kept) == 0 {
		delete(config, "plugin")
	} else {
		config["plugin"] = after
	}
	// What was there before kitt first touched it.
	if backup := path + ".kitt-backup"; data != nil && !exists(backup) {
		if err := os.WriteFile(backup, data, 0o644); err != nil {
			return err
		}
	}
	return writeJSONFile(path, config)
}

// isSpec reports whether a plugin list entry ("spec" or ["spec", options]) is one of specs.
func isSpec(entry any, specs []string) bool {
	if pair, ok := entry.([]any); ok && len(pair) > 0 {
		entry = pair[0]
	}
	name, _ := entry.(string)
	for _, spec := range specs {
		if name == spec {
			return true
		}
	}
	return false
}

func opencodeListed(path, spec string) bool {
	var config struct {
		Plugin []any `json:"plugin"`
	}
	return readJSON(path, &config) == nil && func() bool {
		for _, entry := range config.Plugin {
			if isSpec(entry, []string{spec}) {
				return true
			}
		}
		return false
	}()
}

// hasTUIIntegration says whether the herdr TUI's own opencode plugin, which
// reports an opencode agent's status there, is installed: "ok", "missing", or
// "" when there is no herdr TUI to ask.
func hasTUIIntegration() string {
	if _, err := exec.LookPath("herdr"); err != nil {
		return ""
	}
	out, err := run("", "herdr", "integration", "status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "opencode:") {
			if strings.Contains(line, "not installed") {
				return "missing"
			}
			return "ok"
		}
	}
	return ""
}

// agentIs reports whether an agent herdr lists is of a kind: the TUI names it
// by herdr's kind, the app by what the program reports (claude-code).
func agentIs(agent herdrAgent, kind string) bool {
	name := strings.TrimSuffix(agent.Agent, "-code")
	return name == kind || (kind == "" && name == kindClaude)
}
