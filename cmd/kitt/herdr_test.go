package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPickHerdr(t *testing.T) {
	up, down := func() bool { return true }, func() bool { return false }
	env := func(vars ...string) func(string) string {
		return func(key string) string {
			for i := 0; i+1 < len(vars); i += 2 {
				if vars[i] == key {
					return vars[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		name       string
		mode       string
		getenv     func(string) string
		app, tui   func() bool
		want, says string
	}{
		{"asked app", "app", env(), up, up, "app", "asked for by --herdr"},
		{"asked tui", "tui", env(), up, up, "tui", "asked for by --herdr"},
		{"asked app, not running", "app", env(), down, up, "", "does not answer"},
		{"switched off", "app", env("KITT_NO_HERDR", "1"), up, up, "", "KITT_NO_HERDR"},
		{"inside the app", "auto", env("HC_TERMINAL_ID", "t3", "HERDR_PANE_ID", "p1"), up, up, "app", "inside a herdr app terminal"},
		{"inside the tui", "auto", env("HERDR_PANE_ID", "p1"), up, up, "tui", "inside a herdr pane"},
		{"outside, both up", "auto", env(), up, up, "app", "the herdr app answers"},
		{"outside, tui up", "auto", env(), down, up, "tui", "the herdr server answers"},
		{"in the app, but it is gone", "auto", env("HC_TERMINAL_ID", "t3"), down, up, "tui", "the herdr server answers"},
		{"none", "auto", env(), down, down, "", "neither"},
	}
	for _, c := range cases {
		got, why := pickHerdr(c.mode, "--herdr", c.getenv, c.app, c.tui)
		if got != c.want || !strings.Contains(why, c.says) {
			t.Errorf("%s: got %q (%s), want %q (%s)", c.name, got, why, c.want, c.says)
		}
	}
}

func TestHerdrArgs(t *testing.T) {
	t.Setenv("KITT_HERDR", "")
	defer func() { herdrFlag = "" }()

	rest, err := herdrArgs([]string{"--herdr=app", "ls", "--json"})
	if err != nil || herdrFlag != "app" || strings.Join(rest, " ") != "ls --json" {
		t.Fatalf("--herdr=app: %v %q %v", rest, herdrFlag, err)
	}
	if mode, by := herdrMode(); mode != "app" || by != "--herdr" {
		t.Errorf("mode %s by %s", mode, by)
	}
	rest, err = herdrArgs([]string{"--herdr", "tui", "focus", "x"})
	if err != nil || herdrFlag != "tui" || strings.Join(rest, " ") != "focus x" {
		t.Fatalf("--herdr tui: %v %q %v", rest, herdrFlag, err)
	}
	if _, err := herdrArgs([]string{"--herdr=both"}); err == nil {
		t.Error("--herdr=both is taken")
	}
	// Only in front of the command: later, it belongs to the command.
	rest, _ = herdrArgs([]string{"ls", "--herdr=app"})
	if len(rest) != 2 {
		t.Errorf("a later --herdr was taken: %v", rest)
	}
}

// appSample is `herdr-app ls` as v0.1.16 prints it, with a lane's space and one
// on another machine added.
func appSample(t *testing.T, lanePath, otherPath string) []byte {
	quote := func(s string) string {
		data, _ := json.Marshal(s)
		return string(data)
	}
	return []byte(`{
  "spaces": [
    {
      "id": "sp3281fb5a5f52d27492212e6bf4fa4254",
      "name": "TEST",
      "folder": "C:\\Users\\me",
      "machine": null,
      "active": false,
      "tabs": [
        {"id": "tb86ca7633c985414a00a775d6d318c267", "name": "Claude Code", "active": true, "settled": false,
         "terminals": [{"terminal": "t2", "pane": "pd03b4d024c1a0411b9213ff9e8ffa4f2", "status": "idle", "app": "claude-code", "name": null, "sub_agents": []}]}
      ]
    },
    {
      "id": "splane",
      "name": "412-login",
      "folder": ` + quote(lanePath) + `,
      "machine": null,
      "active": true,
      "tabs": [
        {"id": "tbshell", "name": "1", "active": false, "settled": false,
         "terminals": [{"terminal": "t5", "pane": "pshell", "status": null, "app": null, "name": null, "sub_agents": []}]},
        {"id": "tbclaude", "name": "claude", "active": true, "settled": false,
         "terminals": [{"terminal": "t6", "pane": "pclaude", "status": "blocked", "app": "claude-code", "name": null, "sub_agents": []}]},
        {"id": "tbdev", "name": "dev", "active": false, "settled": false,
         "terminals": [
           {"terminal": "t7", "pane": "pweb", "status": null, "app": null, "name": null, "sub_agents": []},
           {"terminal": "t8", "pane": "papi", "status": null, "app": null, "name": null, "sub_agents": []}]}
      ]
    },
    {
      "id": "vps/spremote",
      "name": "412-login",
      "folder": ` + quote(otherPath) + `,
      "machine": "vps",
      "active": false,
      "tabs": []
    }
  ],
  "recent": []
}`)
}

func TestAppList(t *testing.T) {
	dir := t.TempDir()
	lanePath := filepath.Join(dir, "lanes", "412-login")
	listed := lanePath
	if runtime.GOOS == "windows" {
		// The app may write the folder in another case than git does.
		listed = strings.ToUpper(lanePath[:1]) + strings.ToLower(lanePath[1:])
	}
	list, err := parseAppList(appSample(t, listed, lanePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Spaces) != 2 {
		t.Fatalf("another machine's space is kept: %d spaces", len(list.Spaces))
	}

	lane := Lane{Name: "412-login", Path: lanePath}
	space := list.spaceOf(lane)
	if space == nil || space.ID != "splane" {
		t.Fatalf("the lane's space is not found: %+v", space)
	}
	if list.spaceOf(Lane{Name: "other", Path: filepath.Join(dir, "lanes", "other")}) != nil {
		t.Error("a lane without a space finds one")
	}
	// A space named after the lane, whose terminal went deeper into it.
	deeper, _ := parseAppList(appSample(t, filepath.Join(lanePath, "apps", "web"), lanePath))
	if s := deeper.spaceOf(lane); s == nil || s.ID != "splane" {
		t.Errorf("the lane's space by name is not found: %+v", s)
	}

	agents := appAgents(list, map[string]string{"t6": filepath.Join(lanePath, "apps")})
	if len(agents) != 2 {
		t.Fatalf("agents: %+v", agents)
	}
	agent := agents[1]
	if agent.PaneID != "t6" || agent.Status != "blocked" || agent.WorkspaceID != "splane" || agent.Agent != "claude-code" || !within(agent.Cwd, lanePath) {
		t.Errorf("agent: %+v", agent)
	}
	if agents[0].Cwd != `C:\Users\me` {
		t.Errorf("an agent without a cwd from hc is not where its space is: %q", agents[0].Cwd)
	}
}

func TestParseAppMade(t *testing.T) {
	made, problem := parseAppMade([]byte(`{"space": "sp1", "tab": "tb1", "terminal": "t9"}`))
	if problem != "" || made.Space != "sp1" || made.Tab != "tb1" || made.Terminal != "t9" {
		t.Errorf("made: %+v %q", made, problem)
	}
	made, _ = parseAppMade([]byte(`{"pane": {"id": "p1"}, "terminal": "t3"}`))
	if made.Pane != "p1" || made.Terminal != "t3" {
		t.Errorf("made with an object: %+v", made)
	}
	if _, problem = parseAppMade([]byte("{\n  \"error\": \"no such space\"\n}")); problem != "no such space" {
		t.Errorf("error: %q", problem)
	}
}
