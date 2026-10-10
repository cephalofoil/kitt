package main

import (
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
