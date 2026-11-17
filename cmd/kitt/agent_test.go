package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The lane's agent kind: --with, else the lane's own, else the repo's.
func TestLaneAgentKind(t *testing.T) {
	cfg := RepoConfig{Agent: AgentCfg{Kind: "claude"}}
	lane := Lane{State: &LaneState{}}
	if got := laneAgentKind(lane, cfg, ""); got != "claude" {
		t.Fatalf("repo default: %q", got)
	}
	lane.State.AgentKind = "opencode"
	if got := laneAgentKind(lane, cfg, ""); got != "opencode" {
		t.Fatalf("the lane's own: %q", got)
	}
	if got := laneAgentKind(lane, cfg, "claude"); got != "claude" {
		t.Fatalf("--with: %q", got)
	}
}

// opencode's agents get kitt's folder: pr-watch and the status plugin, listed in
// its configs; Claude gets nothing.
func TestAgentEnvOpencode(t *testing.T) {
	t.Setenv("KITT_HOME", t.TempDir())
	if env, err := agentEnv(kindClaude); err != nil || env != nil {
		t.Fatalf("claude: %v %v", env, err)
	}
	env, err := agentEnv(kindOpencode)
	if err != nil {
		t.Fatal(err)
	}
	dir := env["OPENCODE_CONFIG_DIR"]
	for _, rel := range []string{"kitt-status.js", "pr-watch/server.ts", "pr-watch/steps.ts", "pr-watch/tui.tsx", "pr-watch/shared/parse.ts", "pr-watch/shared/types.d.ts"} {
		if !exists(filepath.Join(dir, filepath.FromSlash(rel))) {
			t.Errorf("%s missing", rel)
		}
	}
	var server struct{ Plugin []string }
	if err := readJSON(filepath.Join(dir, "opencode.json"), &server); err != nil || !slices.Contains(server.Plugin, "./pr-watch/server.ts") || !slices.Contains(server.Plugin, "./kitt-status.js") {
		t.Fatalf("opencode.json: %+v %v", server, err)
	}
	tui, _ := os.ReadFile(filepath.Join(dir, "tui.json"))
	if !strings.Contains(string(tui), "./pr-watch/tui.tsx") {
		t.Fatalf("tui.json: %s", tui)
	}
	// Nothing goes into plugins/: opencode would load pr-watch's helpers as plugins.
	if exists(filepath.Join(dir, "plugins")) {
		t.Fatal("a plugins/ folder was written")
	}
}

// The global install adds kitt's entries beside what is there, keeps the rest,
// backs the file up, and the uninstall takes only kitt's entries out again.
func TestEditOpencodeList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	before := `{"$schema":"https://opencode.ai/config.json","mcp":{"x":{"type":"remote","url":"u"}},"plugin":["mine",["other",{"a":1}]]}`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := editOpencodeList(path, "", true, opencodeServer, opencodeStatus); err != nil {
		t.Fatal(err)
	}
	// Twice is the same as once.
	if err := editOpencodeList(path, "", true, opencodeServer, opencodeStatus); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	list := got["plugin"].([]any)
	if len(list) != 4 || list[0] != "mine" || list[2] != opencodeServer || list[3] != opencodeStatus {
		t.Fatalf("plugin list: %v", list)
	}
	if got["mcp"] == nil {
		t.Fatal("the rest of the config is gone")
	}
	if backup, _ := os.ReadFile(path + ".kitt-backup"); string(backup) != before {
		t.Fatalf("backup: %s", backup)
	}
	if !opencodeListed(path, opencodeServer) {
		t.Fatal("not listed after install")
	}

	if err := editOpencodeList(path, "", false, opencodeServer, opencodeStatus); err != nil {
		t.Fatal(err)
	}
	got = nil
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if list := got["plugin"].([]any); len(list) != 2 || list[0] != "mine" {
		t.Fatalf("after uninstall: %v", list)
	}

	// JSON with comments is not rewritten: the error says what to add.
	jsonc := filepath.Join(t.TempDir(), "opencode.json")
	_ = os.WriteFile(jsonc, []byte("{\n // mine\n}"), 0o644)
	if err := editOpencodeList(jsonc, "", true, opencodeServer); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("jsonc: %v", err)
	}
}

// The dialog's openings for another kind: worded for it, and --with on those
// that start an agent, not on going into the lane or starting its servers.
func TestForKind(t *testing.T) {
	row := LaneView{Lane: Lane{Repo: "r", Name: "a", Managed: true}, Workspace: "w1"}
	list, _ := laneOpenings(row, RepoConfig{}, nil)
	same := forKind(list, kindClaude, kindClaude)
	for i := range list {
		if !slices.Equal(same[i].args, list[i].args) || same[i].label != list[i].label {
			t.Fatalf("the usual kind changed %v", same[i])
		}
	}
	other := forKind(list, kindOpencode, kindClaude)
	for i, o := range other {
		with := slices.Contains(o.args, "--with")
		if startsAgent(list[i]) != with {
			t.Errorf("%q: --with %v", o.label, with)
		}
		if strings.Contains(o.label, "Claude") {
			t.Errorf("%q still says Claude", o.label)
		}
	}
	if !slices.Equal(other[0].args, list[0].args) {
		t.Errorf("going into the lane got %v", other[0].args)
	}
}

// The app names an agent by what it reports, the TUI by herdr's kind.
func TestAgentIs(t *testing.T) {
	for _, c := range []struct {
		agent, kind string
		want        bool
	}{{"claude-code", "claude", true}, {"claude", "claude", true}, {"opencode", "opencode", true}, {"opencode", "claude", false}, {"claude-code", "", true}} {
		if got := agentIs(herdrAgent{Agent: c.agent}, c.kind); got != c.want {
			t.Errorf("agentIs(%s, %s) = %v", c.agent, c.kind, got)
		}
	}
}
