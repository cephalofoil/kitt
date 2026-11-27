package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cmdStatus is `kitt env`, `kitt who` and the lane's database target in one
// answer, for the lane named or the one the directory is in (the main checkout too).
func cmdStatus(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)

	kind := "lane"
	switch {
	case lane.IsMain:
		kind = "main checkout"
	case lane.State != nil && lane.State.ForkOf != "":
		kind = "fork of " + lane.State.ForkOf
	case !lane.Managed:
		kind = "worktree, not a lane yet (kitt adopt)"
	}
	fmt.Printf("lane      %s  (%s)\n", lane.Name, kind)
	fmt.Printf("path      %s\n", lane.Path)
	fmt.Printf("branch    %s\n", orText(lane.Branch, "(none)"))
	if lane.Managed {
		fmt.Printf("slot      %d\n", lane.Slot)
		fmt.Printf("KITT_LANE=%s\nKITT_SLOT=%d\nKITT_ROOT=%s\n", lane.Name, lane.Slot, lane.Path)
		for _, app := range cfg.Apps {
			if app.Port > 0 {
				fmt.Printf("KITT_PORT_%s=%d\n", strings.ToUpper(strings.ReplaceAll(app.Name, "-", "_")), app.port(lane.Slot))
			}
		}
	}

	for _, view := range stacks() {
		if view.Repo != lane.Repo {
			continue
		}
		line := strings.TrimSpace(strings.TrimPrefix(whoLine(view, currentEmulatorLock()), view.Repo))
		held := ""
		if view.Lease != nil && view.Lease.Lane == lane.key() {
			held = "  (this lane)"
		}
		fmt.Printf("stack     %s%s\n", line, held)
	}
	if emu := currentEmulatorLock(); emu != nil {
		fmt.Printf("emulator  %s since %s\n", emu.Name, ago(emu.At))
	} else {
		fmt.Printf("emulator  free\n")
	}

	targets := dbTargets(lane, cfg)
	if len(targets) == 0 {
		fmt.Println("database  no database URL in the lane's env files")
	}
	for _, t := range targets {
		fmt.Printf("database  %s\n", t)
	}
	return nil
}

// dbKey picks the variables that name a database: Supabase or Postgres URLs.
var dbKey = regexp.MustCompile(`(?i)(supabase|database|postgres|(^|_)db)[a-z0-9_]*url$`)

// dbTargets lists, per env file of the lane, which host each database variable
// points at, and the Supabase project ref when there is one: "<file>: KEY ->
// host (project ref)". Only the host and the ref are read out of a value;
// passwords, keys and paths never leave this function.
func dbTargets(lane Lane, cfg RepoConfig) []string {
	var files []string
	seen := map[string]bool{}
	add := func(rel string) {
		path := filepath.Join(lane.Path, filepath.FromSlash(rel))
		if !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	for _, rel := range cfg.Link {
		add(rel)
	}
	for _, app := range cfg.Apps {
		for _, f := range app.EnvFiles {
			add(filepath.ToSlash(filepath.Join(app.Dir, f)))
		}
		if app.EnvOut != "" {
			add(filepath.ToSlash(filepath.Join(app.Dir, app.EnvOut)))
		}
	}

	var out []string
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(lane.Path, path)
		for _, hit := range dbTargetsIn(string(data)) {
			out = append(out, filepath.ToSlash(rel)+": "+hit)
		}
	}
	sort.Strings(out)
	return out
}

// dbTargetsIn reads KEY=value lines and describes each database URL by host
// (and project ref) only.
func dbTargetsIn(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !dbKey.MatchString(key) {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if desc := describeDB(value); desc != "" {
			out = append(out, key+" -> "+desc)
		}
	}
	return out
}

// describeDB is "host" or "host (project ref)" for a URL value, "" for what is not one.
func describeDB(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1", "host.docker.internal":
		return host + " (local)"
	}
	ref := ""
	if strings.HasSuffix(host, ".supabase.co") {
		ref = strings.TrimSuffix(host, ".supabase.co")
		ref = strings.TrimPrefix(ref, "db.")
	} else if user := u.User.Username(); strings.HasPrefix(user, "postgres.") {
		// The pooler puts the project ref after the role: postgres.<ref>.
		ref = strings.TrimPrefix(user, "postgres.")
	}
	if ref != "" && !strings.ContainsAny(ref, " /@:") {
		return host + " (project " + ref + ")"
	}
	return host
}
