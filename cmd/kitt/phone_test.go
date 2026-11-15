package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPhoneEnv(t *testing.T) {
	env, ports := phoneEnv(map[string]string{
		"EXPO_PUBLIC_SUPABASE_URL": "http://localhost:54321",
		"EXPO_PUBLIC_API_URL":      "http://127.0.0.1:8030/v1",
		"EXPO_PUBLIC_SITE":         "https://localhost",
		"EXPO_PUBLIC_REMOTE":       "https://api.example.com",
		"EXPO_PUBLIC_KEY":          "localhost-is-only-a-word-here",
	}, "192.168.1.20")

	want := map[string]string{
		"EXPO_PUBLIC_SUPABASE_URL": "http://192.168.1.20:54321",
		"EXPO_PUBLIC_API_URL":      "http://192.168.1.20:8030/v1",
		"EXPO_PUBLIC_SITE":         "https://192.168.1.20",
		"EXPO_PUBLIC_REMOTE":       "https://api.example.com",
		"EXPO_PUBLIC_KEY":          "localhost-is-only-a-word-here",
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("%s = %q, want %q", key, env[key], value)
		}
	}
	if len(ports) != 2 || ports["EXPO_PUBLIC_SUPABASE_URL"] != 54321 || ports["EXPO_PUBLIC_API_URL"] != 8030 {
		t.Errorf("ports = %v, want the two that name one", ports)
	}
}

func TestPhonePort(t *testing.T) {
	cfg := RepoConfig{Apps: []App{
		{Name: "mobile", Kind: "expo", Port: 8081},
		{Name: "docs", Kind: "web", Port: 8082},
		{Name: "db", Kind: "backend", Port: 8083, Shared: true},
	}}
	// Slot 3: mobile is 8111, docs 8112; the shared db stays on 8083.
	if got := phonePort(cfg, cfg.Apps[0], Lane{Slot: 3}); got != 8113 {
		t.Errorf("phonePort in slot 3 = %d, want 8113", got)
	}
	// The main checkout: 8082 and 8083 are taken.
	if got := phonePort(cfg, cfg.Apps[0], Lane{Slot: 0}); got != 8084 {
		t.Errorf("phonePort in slot 0 = %d, want 8084", got)
	}
}

func TestQR(t *testing.T) {
	code := qr("exp+myapp://expo-development-client/?url=http%3A%2F%2F192.168.1.20%3A8112")
	lines := strings.Split(code, "\n")
	if len(lines) < 15 || len(lines) > 24 {
		t.Errorf("the code is %d lines tall: it has to fit a dashboard pane", len(lines))
	}
	if !strings.Contains(code, "▀") || !strings.Contains(code, "▄") || !strings.Contains(code, "█") {
		t.Error("the code is not drawn with blocks that carry it without colours")
	}
}

// A takeover frees the phone Metro's port with the Metro's, once the lane has had one.
func TestStackPortsIncludePhone(t *testing.T) {
	cfg := RepoConfig{Apps: []App{
		{Name: "mobile", Kind: "expo", Port: 8081, Dev: "expo start"},
		{Name: "api", Kind: "backend", Port: 8000, Dev: "run api"},
	}}
	lane := Lane{Slot: 1}
	names := func(ports []namedPort) string {
		var out []string
		for _, p := range ports {
			out = append(out, p.name+":"+strconv.Itoa(p.port))
		}
		return strings.Join(out, " ")
	}
	if got := names(stackPorts(lane, cfg, stackApps(cfg), nil)); got != "mobile:8091 api:8010" {
		t.Errorf("no phone Metro: %s", got)
	}
	entry := &LaneState{Phone: "192.168.1.20", PhonePane: "p9"}
	if got := names(stackPorts(lane, cfg, stackApps(cfg), entry)); got != "mobile:8091 api:8010 mobile phone:8092" {
		t.Errorf("with a phone Metro: %s", got)
	}
	cfg.Stack.Keep = []string{"mobile"}
	if got := names(stackPorts(lane, cfg, stackApps(cfg), entry)); got != "api:8010" {
		t.Errorf("a kept Metro keeps its phone twin: %s", got)
	}
}

// kitt phone starts a server for the lane: in the single mode it takes the stack
// as `kitt up` does, and fails against another lane's live hard lease.
func TestPhoneTakesStack(t *testing.T) {
	w := newStackWorld(t, "single")
	dir := t.TempDir()
	toml := "name = \"r\"\nbase = \"main\"\n[stack]\nmode = \"single\"\n[[app]]\nname = \"mobile\"\nkind = \"expo\"\nport = 8081\ndev = \"expo start --port {port}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kitt.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	lane := Lane{Repo: "r", Main: dir, Path: dir, Name: "p", Slot: 4, Managed: true}
	oldHerdr := herdrFound
	t.Cleanup(func() { herdrFound = oldHerdr })

	// Printed only: nothing is started, nothing is taken.
	off := false
	herdrFound = &off
	if _, err := phone(lane, "192.0.2.10", true); err != nil {
		t.Fatal(err)
	}
	if exists(leasePath("r")) {
		t.Fatal("kitt phone --print took the stack")
	}
	if _, err := phone(lane, "192.0.2.10", false); err == nil || !strings.Contains(err.Error(), "needs herdr") {
		t.Fatalf("without herdr: %v", err)
	}

	// With herdr (none on PATH, so nothing is opened): another lane's test keeps the phone out.
	t.Setenv("PATH", "")
	on := true
	herdrFound = &on
	_, _ = w.acquire("a", "hard", 0, false)
	if _, err := phone(lane, "192.0.2.10", false); err == nil || !strings.Contains(err.Error(), "held by a") {
		t.Fatalf("against a's hard lease: %v", err)
	}
	if w.lease().Name != "a" || len(w.stops()) != 0 {
		t.Fatal("the phone took a held stack")
	}
	_, _ = releaseStack(w.lane("a"), false)
	_, _ = phone(lane, "192.0.2.10", false)
	if l := w.lease(); l.Name != "p" || l.Kind != "soft" || l.Reason != "phone" {
		t.Fatalf("lease after kitt phone: %+v", l)
	}
	if got := w.stops(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("stopped %v, want [a]", got)
	}
}
