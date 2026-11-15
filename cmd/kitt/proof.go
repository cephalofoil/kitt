package main

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// A proof is the evidence that a lane's change works in the running app: a run
// of labelled screenshots and a verdict, made by whoever did the work.

func proofRoot(lane Lane) string {
	return filepath.Join(configDir(), "proofs", lane.Repo, lane.Name)
}

func cmdProof(args []string) error {
	if len(args) == 0 {
		return fail("usage: kitt proof begin | shot <label> | add <file> [label] | end --pass|--fail [--note text] | open [lane] | status")
	}
	sub, args := args[0], args[1:]
	rest, opts := flags(args, "pass", "fail", "no-emulator")

	laneArg := opts["lane"]
	if sub == "open" || sub == "status" {
		laneArg = strings.Join(rest, "")
	}
	lane, err := findLane(laneArg)
	if err != nil {
		return err
	}
	if !lane.Managed {
		return fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	current := func() (*ProofState, error) {
		if s := loadState().Lanes[lane.key()]; s != nil && s.Proof != nil && s.Proof.Status == "running" {
			return s.Proof, nil
		}
		return nil, fail("no proof is running for %s: kitt proof begin", lane.Name)
	}

	switch sub {
	case "begin":
		return proofBegin(lane, opts)

	case "shot":
		proof, err := current()
		if err != nil {
			return err
		}
		label := slug(strings.Join(rest, " "), 48)
		if label == "" {
			return fail("usage: kitt proof shot <label>")
		}
		renewStack(lane)
		dev, err := pickDevice(loadRepoConfig(lane.Main, lane.Path))
		if err != nil {
			return err
		}
		png, err := dev.screenshot()
		if err != nil || len(png) < 1000 {
			return fail("the screenshot failed")
		}
		return proofStore(lane, proof, label, ".png", png)

	case "add":
		proof, err := current()
		if err != nil {
			return err
		}
		if len(rest) == 0 {
			return fail("usage: kitt proof add <file> [label]")
		}
		renewStack(lane)
		data, err := os.ReadFile(rest[0])
		if err != nil {
			return err
		}
		label := slug(strings.TrimSuffix(filepath.Base(rest[0]), filepath.Ext(rest[0])), 48)
		if len(rest) > 1 {
			label = slug(strings.Join(rest[1:], " "), 48)
		}
		return proofStore(lane, proof, label, strings.ToLower(filepath.Ext(rest[0])), data)

	case "end":
		proof, err := current()
		if err != nil {
			return err
		}
		if (opts["pass"] == "") == (opts["fail"] == "") {
			return fail("say how it went: kitt proof end --pass, or --fail --note \"what is wrong\"")
		}
		verdict := "pass"
		if opts["fail"] != "" {
			verdict = "fail"
		}
		if notes, err := releaseStack(lane, false); err == nil {
			for _, note := range notes {
				fmt.Println(note)
			}
		}
		head, _ := run(lane.Path, "git", "rev-parse", "HEAD")
		tree := treeOf(lane.Path)
		var done ProofState
		if err := updateState(func(s *State) {
			if entry := s.Lanes[lane.key()]; entry != nil && entry.Proof != nil {
				entry.Proof.Status, entry.Proof.Note = verdict, opts["note"]
				entry.Proof.Ended, entry.Proof.Commit, entry.Proof.Tree = time.Now(), head, tree
				done = *entry.Proof
			}
		}); err != nil {
			return err
		}
		page, err := proofPage(lane, done)
		if err != nil {
			return err
		}
		fmt.Printf("proof %s  %d shots  %s\n", verdict, proof.Shots, page)
		return nil

	case "open":
		entry := loadState().Lanes[lane.key()]
		if entry == nil || entry.Proof == nil {
			return fail("%s has no proof yet", lane.Name)
		}
		page := filepath.Join(entry.Proof.Dir, "index.html")
		if !exists(page) {
			if _, err := proofPage(lane, *entry.Proof); err != nil {
				return err
			}
		}
		return openFile(page)

	case "status":
		entry := loadState().Lanes[lane.key()]
		if entry == nil || entry.Proof == nil {
			fmt.Printf("%s: no proof\n", lane.Name)
			return nil
		}
		p := entry.Proof
		stale := ""
		if p.Status != "running" && treeOf(lane.Path) != p.Tree {
			stale = "  (stale: the code changed since)"
		}
		fmt.Printf("%s: %s  %d shots  %s ago%s\n  %s\n", lane.Name, p.Status, p.Shots, ago(p.Started), stale, p.Dir)
		if p.Note != "" {
			fmt.Printf("  %s\n", p.Note)
		}
		return nil
	}
	return fail("unknown: kitt proof %s", sub)
}

func proofBegin(lane Lane, opts map[string]string) error {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	useEmulator := lane.hasExpo(cfg) && opts["no-emulator"] == ""

	wait := 10 * time.Minute
	if value, err := time.ParseDuration(opts["wait"]); err == nil {
		wait = value
	}
	// The single mode: the proof holds the stack (and the emulator) until it ends.
	_, notes, err := acquireStack(lane, cfg, "hard", "proof", wait, false)
	if err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Println(note)
	}
	if cfg.Stack.single() && useEmulator {
		for _, note := range startMetro(lane, cfg) {
			fmt.Println(note)
		}
	}

	if useEmulator {
		if !cfg.Stack.single() {
			if err := acquireEmulator(lane, wait); err != nil {
				return err
			}
		}
		message, err := pointEmulator(lane)
		if err != nil {
			_, _ = releaseStack(lane, false)
			return err
		}
		fmt.Println(message)
		time.Sleep(time.Duration(cfg.Proof.WaitMs) * time.Millisecond)
	}

	dir := filepath.Join(proofRoot(lane), time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := updateState(func(s *State) {
		entry := s.Lanes[lane.key()]
		if entry == nil {
			// The main checkout is a lane without an entry until something is kept for it.
			entry = &LaneState{Repo: lane.Repo, Name: lane.Name, Path: lane.Path, Slot: lane.Slot, Created: time.Now()}
			s.Lanes[lane.key()] = entry
		}
		entry.Proof = &ProofState{Dir: dir, Status: "running", Started: time.Now()}
	}); err != nil {
		return err
	}

	fmt.Printf("proof started for %s\n  %s\n", lane.Name, dir)
	if useEmulator {
		fmt.Println("  the emulator is yours until `kitt proof end`")
		if dev, err := pickDevice(cfg); err == nil {
			fmt.Println("  " + dev.driveHint())
		}
	} else if cfg.Stack.single() {
		fmt.Println("  the stack is yours until `kitt proof end`")
	}
	fmt.Println("  kitt proof shot <label>   after each step worth showing")
	fmt.Println("  kitt proof end --pass     or --fail --note \"what is wrong\"")
	if guide := proofGuide(lane, cfg); guide != "" {
		fmt.Printf("\n%s\n", guide)
	}
	return nil
}

// proofGuide is the repo's own word on proving a change: inline text, or a file
// in the repo (test accounts, how to reach a screen).
func proofGuide(lane Lane, cfg RepoConfig) string {
	guide := strings.TrimSpace(cfg.Proof.Guide)
	if guide == "" || strings.Contains(guide, "\n") {
		return guide
	}
	if data, err := os.ReadFile(filepath.Join(lane.Path, filepath.FromSlash(guide))); err == nil {
		return strings.TrimSpace(string(data))
	}
	return guide
}

func proofStore(lane Lane, proof *ProofState, label, ext string, data []byte) error {
	var name string
	if err := updateState(func(s *State) {
		entry := s.Lanes[lane.key()]
		if entry == nil || entry.Proof == nil {
			return
		}
		entry.Proof.Shots++
		name = fmt.Sprintf("%02d-%s%s", entry.Proof.Shots, label, ext)
	}); err != nil {
		return err
	}
	path := filepath.Join(proof.Dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// proofPage writes the run as one page to look through: verdict, note, shots in order.
func proofPage(lane Lane, proof ProofState) (string, error) {
	entries, err := os.ReadDir(proof.Dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, entry := range entries {
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif":
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	title := lane.Name
	if lane.State != nil && lane.State.Title != "" {
		title = lane.State.Title
		if lane.State.Issue > 0 {
			title = fmt.Sprintf("#%d %s", lane.State.Issue, lane.State.Title)
		}
	}
	color := map[string]string{"pass": "#1a7f37", "fail": "#cf222e"}[proof.Status]
	commit := proof.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html><meta charset="utf-8"><title>Proof · %s</title>
<style>
body{font:15px/1.5 system-ui,sans-serif;margin:32px;background:#f6f8fa;color:#1f2328}
h1{font-size:20px;margin:0 0 4px}
.meta{color:#59636e;margin-bottom:6px}
.verdict{display:inline-block;padding:2px 10px;border-radius:999px;color:#fff;background:%s;font-weight:600}
.note{margin:12px 0;padding:10px 14px;background:#fff;border:1px solid #d1d9e0;border-radius:8px;white-space:pre-wrap}
.shots{display:flex;flex-wrap:wrap;gap:20px;margin-top:20px}
figure{margin:0;background:#fff;border:1px solid #d1d9e0;border-radius:10px;padding:10px}
figure img{height:560px;display:block;border-radius:6px}
figcaption{margin-top:8px;font-weight:600}
@media(prefers-color-scheme:dark){body{background:#0d1117;color:#f0f6fc}.note,figure{background:#151b23;border-color:#3d444d}.meta{color:#9198a1}}
</style>
<h1>%s</h1>
<div class="meta">%s · %s · %s · %s</div>
<span class="verdict">%s</span>
`, html.EscapeString(title), color, html.EscapeString(title), html.EscapeString(lane.Repo), html.EscapeString(lane.Branch),
		commit, proof.Started.Format("2006-01-02 15:04"), proof.Status)
	if proof.Note != "" {
		fmt.Fprintf(&b, `<div class="note">%s</div>`, html.EscapeString(proof.Note))
	}
	b.WriteString(`<div class="shots">`)
	for _, file := range files {
		label := strings.TrimSuffix(file, filepath.Ext(file))
		if len(label) > 3 {
			label = strings.ReplaceAll(label[3:], "-", " ")
		}
		fmt.Fprintf(&b, `<figure><a href="%s"><img src="%s" alt="%s"></a><figcaption>%s</figcaption></figure>`,
			file, file, html.EscapeString(label), html.EscapeString(label))
	}
	b.WriteString(`</div>`)

	page := filepath.Join(proof.Dir, "index.html")
	return page, os.WriteFile(page, []byte(b.String()), 0o644)
}

func openFile(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
