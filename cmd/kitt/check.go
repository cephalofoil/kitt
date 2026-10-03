package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The checks of an app are the repo's own: kitt.toml names them (or the app's
// package.json scripts do), kitt only decides which apps a lane touched.

// touchedApps are the apps with files changed on the lane: committed since it
// left the base branch, or not committed yet.
func touchedApps(lane Lane, cfg RepoConfig) []App {
	var files []string
	if base, err := run(lane.Path, "git", "merge-base", "HEAD", "origin/"+cfg.Base); err == nil {
		if out, err := run(lane.Path, "git", "diff", "--name-only", base); err == nil {
			files = append(files, strings.Split(out, "\n")...)
		}
	}
	if out, err := run(lane.Path, "git", "ls-files", "--others", "--exclude-standard"); err == nil {
		files = append(files, strings.Split(out, "\n")...)
	}

	var apps []App
	for _, app := range cfg.Apps {
		prefix := strings.Trim(filepath.ToSlash(app.Dir), "/") + "/"
		for _, file := range files {
			if file = strings.TrimSpace(file); file != "" && (app.Dir == "." || strings.HasPrefix(file, prefix)) {
				apps = append(apps, app)
				break
			}
		}
	}
	return apps
}

func cmdCheck(args []string) error {
	rest, opts := flags(args, "all")
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	result, report := check(lane, opts["all"] != "", opts["app"], true)
	if report != "" {
		fmt.Print(report)
	}
	if len(result.Failed) > 0 {
		return fail("%d of %d checks failed", len(result.Failed), result.Passed+len(result.Failed))
	}
	return nil
}

// check runs the checks of the lane's touched apps one after the other. With
// `live`, each result is printed as it lands and the report is empty.
func check(lane Lane, all bool, only string, live bool) (CheckResult, string) {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	apps := touchedApps(lane, cfg)
	if all {
		apps = cfg.Apps
	}

	var report strings.Builder
	say := func(format string, args ...any) {
		if live {
			fmt.Printf(format, args...)
		} else {
			fmt.Fprintf(&report, format, args...)
		}
	}

	head, _ := run(lane.Path, "git", "rev-parse", "HEAD")
	result := CheckResult{At: time.Now(), Commit: head, Tree: treeOf(lane.Path)}
	ran := 0
	for _, app := range apps {
		if only != "" && app.Name != only {
			continue
		}
		// A lane has every app's code, the shared ones included: check it where the change is.
		dir := filepath.Join(lane.Path, filepath.FromSlash(app.Dir))
		if len(app.Checks) > 0 {
			ensureSetup(lane, cfg, app)
		}
		for _, line := range app.Checks {
			line = cfg.expand(line, app, lane)
			ran++
			started := time.Now()
			cmd := shell(dir, line)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			took := time.Since(started).Round(100 * time.Millisecond)

			if err == nil {
				result.Passed++
				say("✓ %-8s %s  %s\n", app.Name, line, took)
				continue
			}
			result.Failed = append(result.Failed, app.Name+": "+line)
			say("✗ %-8s %s  %s\n", app.Name, line, took)
			lines := strings.Split(strings.TrimRight(out.String(), "\r\n"), "\n")
			if len(lines) > 30 {
				lines = lines[len(lines)-30:]
			}
			for _, l := range lines {
				say("    %s\n", strings.TrimRight(l, "\r"))
			}
		}
	}
	if ran == 0 {
		say("no checks to run: no app with checks was touched on this lane (kitt check --all runs every app)\n")
	}

	_ = updateState(func(s *State) {
		entry := s.Lanes[lane.key()]
		if entry == nil {
			entry = &LaneState{Repo: lane.Repo, Name: lane.Name, Path: lane.Path, Slot: lane.Slot, Created: time.Now()}
			s.Lanes[lane.key()] = entry
		}
		if ran > 0 {
			entry.Checks = &result
		}
	})

	return result, report.String()
}
