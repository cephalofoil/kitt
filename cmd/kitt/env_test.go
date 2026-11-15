package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// env_out carries only what the config sets for the lane, never the env files'
// secrets, with the lane's ports filled in.
func TestEnvOutWritesLaneValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=hunter2\nEXPO_PUBLIC_API_URL=http://10.0.0.18:8000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := RepoConfig{Apps: []App{
		{Name: "api", Port: 8000},
		{Name: "mobile", Kind: "expo", Port: 8081, PortEnv: "RCT_METRO_PORT", EnvFiles: []string{".env"}, EnvOut: ".env.development.local",
			Env: map[string]string{"EXPO_PUBLIC_API_URL": "http://localhost:{port:api}", "EXPO_PUBLIC_LANE": "{lane} #{slot}"}},
	}}
	app := cfg.Apps[1]
	lane := Lane{Name: "seven", Slot: 7, Path: dir, Main: dir}

	process := appEnv(cfg, app, lane, dir)
	if process["SECRET"] != "hunter2" || process["EXPO_PUBLIC_API_URL"] != "http://localhost:8070" {
		t.Fatalf("process env %v: the env file, then the config over it", process)
	}

	path := envOutPath(app, dir)
	if err := writeEnvOut(path, laneEnv(cfg, app, lane)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Written by kitt up for this lane; kitt rewrites it on every start.\n" +
		"EXPO_PUBLIC_API_URL=http://localhost:8070\n" +
		"EXPO_PUBLIC_LANE=\"seven #7\"\n" +
		"RCT_METRO_PORT=8151\n"
	if string(data) != want {
		t.Fatalf("env_out:\n%s\nwant:\n%s", data, want)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
}

// A linked env file is the main checkout's: writing a lane's ports into it would change every lane.
func TestEnvOutRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.env")
	if err := os.WriteFile(target, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".env.local")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := writeEnvOut(link, map[string]string{"A": "2"}); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if data, _ := os.ReadFile(target); string(data) != "A=1\n" {
		t.Fatalf("the linked file changed: %q", data)
	}
}

func TestEnvOutIgnoredCheck(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env*.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !envOutIgnored(filepath.Join(dir, ".env.development.local")) {
		t.Fatal("an ignored file reads as not ignored")
	}
	if envOutIgnored(filepath.Join(dir, ".env.lane")) {
		t.Fatal("a file git would commit reads as ignored")
	}
}

// A changed env or command makes a different hash, so `up` restarts the app.
func TestDevHash(t *testing.T) {
	app := App{Name: "mobile", EnvOut: ".env.development.local"}
	base := devHash("expo start --port 8151", "/l", app, map[string]string{"A": "1", "B": "2"})
	if base != devHash("expo start --port 8151", "/l", app, map[string]string{"B": "2", "A": "1"}) {
		t.Fatal("the hash depends on map order")
	}
	if base == devHash("expo start --port 8151", "/l", app, map[string]string{"A": "1", "B": "3"}) {
		t.Fatal("a changed env kept the hash")
	}
	if base == devHash("expo start --port 8161", "/l", app, map[string]string{"A": "1", "B": "2"}) {
		t.Fatal("a changed command kept the hash")
	}
}

// What envValue writes for each value, pinned. Every want was read back as the
// value by dotenv 16.6.1 with dotenv-expand 11.0.7 and by @expo/env 2.6.0's
// parse (Node 22's util.parseEnv) and expand, as "A=<want>" between other lines;
// the values with an error have no quoting both read back. Change a want only
// after checking it against those parsers again.
func TestEnvValue(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"http://localhost:8070", "http://localhost:8070"},
		{"a b", `"a b"`},
		{"seven#7", `"seven#7"`},
		{"it's", `"it's"`},
		{`say "hi"`, `'say "hi"'`},
		{"a`b", "\"a`b\""},
		{"pa$$word", `pa\$\$word`},
		{"$HOME/x", `\$HOME/x`},
		{"${API}", `\${API}`},
		{`a\$b`, `a\\\$b`},
		{`cost \$5`, `'cost \\\$5'`},
		{"two\nlines", "\"two\nlines\""},
		{`C:\new`, `C:\new`},
		{`C:\Program Files\node`, `'C:\Program Files\node'`},
		{`it's "x"`, "`it's \"x\"`"},
		{"line\nwith \"quote\"", "'line\nwith \"quote\"'"},
	} {
		got, err := envValue(c.in)
		if err != nil || got != c.want {
			t.Errorf("envValue(%q) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"it's \"x\" `y`", "it's `y` C:\\n", "cr\rlf", `$A\$B`} {
		if got, err := envValue(in); err == nil {
			t.Errorf("envValue(%q) = %s, want an error", in, got)
		}
	}
	// writeEnvOut names the key it cannot write, and writes nothing.
	path := filepath.Join(t.TempDir(), ".env.development.local")
	err := writeEnvOut(path, map[string]string{"OK": "1", "BAD": "it's \"x\" `y`"})
	if err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Errorf("writeEnvOut: %v, want an error naming BAD", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("writeEnvOut wrote a file it could not write whole")
	}
}

// Expo reads .env.development.local, .env.local, .env.development, .env in that
// order: a linked file above env_out wins with the main checkout's values.
func TestEnvOutOutranked(t *testing.T) {
	cfg := RepoConfig{Link: []string{"apps/mobile/.env.local", "apps/mobile/.env", "apps/web/.env.development.local", ".env.local"}}
	mobile := App{Name: "mobile", Kind: "expo", Dir: "apps/mobile"}
	for _, c := range []struct{ envOut, want string }{
		{"", ""},
		{".env.development.local", ""},
		{".env.development", ".env.local"},
		{".env.lane", ".env.local .env"},
	} {
		mobile.EnvOut = c.envOut
		if got := strings.Join(envOutOutranked(cfg, mobile), " "); got != c.want {
			t.Errorf("env_out %q: outranked by %q, want %q", c.envOut, got, c.want)
		}
	}
	mobile.EnvOut = ".env.development"
	if w := envOutWarning(cfg, mobile); !strings.Contains(w, "linked .env.local before env_out .env.development") || !strings.Contains(w, "name env_out .env.development.local") {
		t.Errorf("warning %q", w)
	}

	// The app at the repo's root, and a kind whose bundler kitt cannot tell (Vite and Next differ).
	root := App{Name: "app", Kind: "expo", Dir: ".", EnvOut: ".env"}
	if got := strings.Join(envOutOutranked(cfg, root), " "); got != ".env.local" {
		t.Errorf("an app at the root: %q", got)
	}
	root.Kind = "web"
	if got := envOutOutranked(cfg, root); got != nil {
		t.Errorf("a web app: %v", got)
	}
}
