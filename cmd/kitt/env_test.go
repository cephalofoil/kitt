package main

import (
	"os"
	"path/filepath"
	"runtime"
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
