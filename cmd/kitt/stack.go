package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// A repo whose kitt.toml says `[stack] mode = "single"` runs one lane's servers
// at a time. The lease says which lane that is, since when and why.
//
// A soft lease is what `kitt focus` and `kitt up` leave behind: the next lane to
// go there stops this one's servers and takes over. A hard lease is an agent's
// test (`kitt proof begin`, `kitt claim`): others wait for it, or fail, unless a
// person says --force. Releasing a hard lease makes it soft again.

type Lease struct {
	Repo   string `json:"repo"`
	Lane   string `json:"lane"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	// Holder is the process the hard lease lives as long as: the agent's session.
	Holder  int       `json:"holder"`
	Since   time.Time `json:"since"`
	Renewed time.Time `json:"renewed"`
	// Path, Main and Slot are the holder's lane, so a lane taking over can stop its servers.
	Path string `json:"path"`
	Main string `json:"main"`
	Slot int    `json:"slot"`
}

// What the lease logic reaches outside itself for, so a test can stand in.
var (
	stopper = stopStack
	alive   = processAlive
	now     = time.Now
	poll    = 3 * time.Second
)

func leasePath(repo string) string { return filepath.Join(configDir(), "stack-"+repo+".lock") }

func stackMutex(repo string) string { return "stack-" + repo + "-mutex" }

func readLease(repo string) (Lease, bool) {
	var l Lease
	if readJSON(leasePath(repo), &l) != nil || l.Lane == "" {
		return Lease{}, false
	}
	return l, true
}

// blocks reports whether a lease keeps other lanes out: hard, its holder still
// running and heard from within the ttl.
func (l Lease) blocks(ttl time.Duration) bool {
	return l.Kind == "hard" && alive(l.Holder) && now().Sub(l.Renewed) < ttl
}

func (l Lease) lane() Lane {
	return Lane{Repo: l.Repo, Main: l.Main, Path: l.Path, Name: l.Name, Slot: l.Slot, Managed: true}
}

func (l Lease) held() string {
	return fmt.Sprintf("%s (%s, since %s)", l.Name, l.Reason, ago(l.Since))
}

// holderPid is the process a hard lease is tied to. kitt's parent is often a
// shell made for this one command (an agent's tool call) that ends with it;
// the grandparent is the session that made it, a person's terminal or an agent.
func holderPid() int {
	parent := os.Getppid()
	if runtime.GOOS == "windows" {
		return parent
	}
	out, err := run("", "ps", "-o", "ppid=", "-p", strconv.Itoa(parent))
	if grand, perr := strconv.Atoi(strings.TrimSpace(out)); err == nil && perr == nil && grand > 1 {
		return grand
	}
	return parent
}

// acquireStack takes the repo's stack for a lane. A lane holding a hard lease
// elsewhere is waited for up to `wait`, then named in the error; any other is
// displaced: its servers are stopped and the lease is the lane's. Outside the
// single mode it does nothing.
func acquireStack(lane Lane, cfg RepoConfig, kind, reason string, wait time.Duration, force bool) (*Lease, []string, error) {
	if !cfg.Stack.single() {
		return nil, nil, nil
	}
	ttl := cfg.Stack.ttl()
	// The wait is real time; `now` is the lease's own clock.
	deadline := time.Now().Add(wait)
	announced := false
	for {
		var displaced, blocker *Lease
		var busy *emuLock
		err := locked(stackMutex(lane.Repo), func() error {
			held, ok := readLease(lane.Repo)
			other := ok && held.Lane != lane.key()
			if other && held.blocks(ttl) && !force {
				blocker = &held
				return nil
			}
			at := now()
			next := Lease{Repo: lane.Repo, Lane: lane.key(), Name: lane.Name, Kind: kind, Reason: reason,
				Holder: holderPid(), Since: at, Renewed: at, Path: lane.Path, Main: lane.Main, Slot: lane.Slot}
			if ok && !other {
				// The holder again: the lease is renewed, and a soft take keeps a hard lease hard.
				next.Since = held.Since
				if kind == "soft" {
					next.Kind, next.Reason, next.Holder = held.Kind, held.Reason, held.Holder
				}
			}
			replaced := ""
			if other {
				displaced, replaced = &held, held.Lane
			}
			if next.Kind == "hard" && lane.hasExpo(cfg) {
				// The emulator is one device for every repo: a hard lease takes it too.
				var err error
				if busy, err = takeEmulator(lane, replaced, force); err != nil || busy != nil {
					return err
				}
			} else if replaced != "" {
				releaseEmulatorOf(replaced)
			}
			return writeJSON(leasePath(lane.Repo), next)
		})
		if err != nil {
			return nil, nil, err
		}
		if blocker == nil && busy == nil {
			var notes []string
			if displaced != nil {
				notes = append(notes, "stopped "+displaced.Name)
				for _, line := range stopper(displaced.lane(), cfg) {
					notes = append(notes, "  "+line)
				}
			}
			return displaced, notes, nil
		}

		what := ""
		if blocker != nil {
			what = "the stack of " + lane.Repo + " is held by " + blocker.held()
		} else {
			what = fmt.Sprintf("the emulator is held by %s (since %s)", busy.Name, ago(busy.At))
		}
		if !time.Now().Before(deadline) {
			return nil, nil, fail("%s: wait for it (kitt claim --wait 10m), or ask its holder to `kitt release`", what)
		}
		if !announced {
			fmt.Printf("waiting: %s\n", what)
			announced = true
		}
		time.Sleep(min(poll, time.Until(deadline)+time.Millisecond))
	}
}

// needsHerdr fails in the single mode without herdr: kitt starts servers only
// in herdr panes, and a stack it did not start it cannot be sure to stop.
func needsHerdr(cfg RepoConfig) error {
	if cfg.Stack.single() && !hasHerdr() {
		return fail("the single mode needs herdr to start and stop a lane's servers (or kitt up --print, and run them yourself)")
	}
	return nil
}

// releaseStack hands the stack back: a hard lease becomes soft, the servers
// keep running and the next lane may take them over. With `down` the servers
// stop and the lease is gone. It always gives the emulator back.
func releaseStack(lane Lane, down bool) ([]string, error) {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	var notes []string
	err := locked(stackMutex(lane.Repo), func() error {
		releaseEmulatorOf(lane.key())
		held, ok := readLease(lane.Repo)
		switch {
		case !ok || held.Lane != lane.key():
			if cfg.Stack.single() && !down {
				if ok {
					notes = append(notes, lane.Name+" does not hold the stack: "+held.held()+" does")
				} else {
					notes = append(notes, "the stack of "+lane.Repo+" is free")
				}
			}
			return nil
		case down:
			return os.Remove(leasePath(lane.Repo))
		}
		held.Kind, held.Renewed = "soft", now()
		notes = append(notes, "released: "+lane.Name+"'s stack keeps running, another lane may take it")
		return writeJSON(leasePath(lane.Repo), held)
	})
	if err == nil && down {
		notes = append(notes, stopper(lane, cfg)...)
	}
	return notes, err
}

// renewStack tells the lease its holder is still at work.
func renewStack(lane Lane) {
	_ = locked(stackMutex(lane.Repo), func() error {
		held, ok := readLease(lane.Repo)
		if !ok || held.Lane != lane.key() {
			return nil
		}
		held.Renewed = now()
		return writeJSON(leasePath(lane.Repo), held)
	})
	_ = locked(emuMutex, func() error {
		var held emuLock
		if readJSON(lockPath(), &held) != nil || held.Lane != lane.key() {
			return nil
		}
		held.At = now()
		return writeJSON(lockPath(), held)
	})
}

// dropLease forgets a lane's lease, as its servers have been stopped.
func dropLease(lane Lane) {
	_ = locked(stackMutex(lane.Repo), func() error {
		if held, ok := readLease(lane.Repo); ok && held.Lane == lane.key() {
			releaseEmulatorOf(lane.key())
			return os.Remove(leasePath(lane.Repo))
		}
		return nil
	})
}

// stackApps are the apps the single mode stops: all but the shared ones and
// those the repo keeps.
func stackApps(cfg RepoConfig) []App {
	keep := map[string]bool{}
	for _, name := range cfg.Stack.Keep {
		keep[name] = true
	}
	var apps []App
	for _, app := range cfg.Apps {
		if !app.Shared && !keep[app.Name] {
			apps = append(apps, app)
		}
	}
	return apps
}

// stopStack stops a lane's stack apps and makes sure their ports are free. The
// dev tab is closed when nothing kitt keeps running could live in it; else
// each app's stop line and its port are what stop it.
func stopStack(lane Lane, cfg RepoConfig) []string {
	apps := stackApps(cfg)
	closeTab := true
	for _, app := range cfg.Apps {
		kept := true
		for _, stacked := range apps {
			kept = kept && stacked.Name != app.Name
		}
		if kept && app.Dev != "" && listening(app.port(lane.Slot)) {
			closeTab = false
		}
	}
	lines := stopApps(lane, cfg, apps, closeTab)

	for _, app := range apps {
		port := app.port(lane.Slot)
		if port <= 0 {
			continue
		}
		for waited := 0; waited < 20 && listening(port); waited++ {
			time.Sleep(500 * time.Millisecond)
		}
		if !listening(port) {
			continue
		}
		if pids := listeners(port); len(pids) > 0 {
			for _, pid := range pids {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
			lines = append(lines, fmt.Sprintf("%-8s still on %d: killed %s", app.Name, port, joinInts(pids)))
		} else {
			lines = append(lines, fmt.Sprintf("%-8s still on %d: stop it by hand", app.Name, port))
		}
	}
	return lines
}

// listeners are the pids listening on a port, as lsof sees them.
func listeners(port int) []int {
	if runtime.GOOS == "windows" {
		return nil
	}
	out, _ := run("", "lsof", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
	var pids []int
	for _, field := range strings.Fields(out) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, " ")
}

// --- kitt claim / release / who ----------------------------------------------

// cmdClaim takes the stack for an agent's test that is not a proof (curl
// against the api, maestro, playwright) and starts the lane's servers.
func cmdClaim(args []string) error {
	rest, opts := flags(args, "force")
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	if !lane.Managed {
		return fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	if !cfg.Stack.single() {
		return fail("%s runs every lane's stack side by side: there is nothing to claim ([stack] mode = \"single\" in kitt.toml)", lane.Repo)
	}
	if err := needsHerdr(cfg); err != nil {
		return err
	}
	wait, _ := time.ParseDuration(opts["wait"])
	reason := "claim"
	if text := strings.TrimSpace(opts["reason"]); text != "" && text != "true" {
		reason = "claim: " + text
	}
	_, notes, err := acquireStack(lane, cfg, "hard", reason, wait, opts["force"] != "")
	if err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Println(note)
	}
	lines, err := up(lane, nil, false)
	for _, line := range lines {
		fmt.Println(line)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s holds the stack of %s until `kitt release`\n", lane.Name, lane.Repo)
	return nil
}

func cmdRelease(args []string) error {
	rest, opts := flags(args, "down")
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	notes, err := releaseStack(lane, opts["down"] != "")
	for _, note := range notes {
		fmt.Println(note)
	}
	return err
}

// stackView is one repo's stack as `kitt who` and the dashboard show it.
type stackView struct {
	Repo  string `json:"repo"`
	Mode  string `json:"mode"`
	Lease *Lease `json:"lease"`
	// Stale says a hard lease no longer keeps anyone out: its holder ended, or went quiet.
	Stale bool `json:"stale,omitempty"`
}

func stacks() []stackView {
	var out []stackView
	for _, repo := range loadGlobal().Repos {
		cfg := loadRepoConfig(repo.Path, "")
		view := stackView{Repo: repo.Name, Mode: "parallel"}
		if cfg.Stack.single() {
			view.Mode = "single"
			if lease, ok := readLease(repo.Name); ok {
				view.Lease = &lease
				view.Stale = lease.Kind == "hard" && !lease.blocks(cfg.Stack.ttl())
			}
		}
		out = append(out, view)
	}
	return out
}

func currentEmulatorLock() *emuLock {
	var held emuLock
	if readJSON(lockPath(), &held) != nil || !held.fresh() {
		return nil
	}
	return &held
}

func cmdWho(args []string) error {
	_, opts := flags(args, "json")
	views, emu := stacks(), currentEmulatorLock()
	if opts["json"] != "" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Stacks   []stackView `json:"stacks"`
			Emulator *emuLock    `json:"emulator"`
		}{views, emu})
	}
	for _, view := range views {
		fmt.Println(whoLine(view, emu))
	}
	if emu != nil {
		fmt.Printf("%-16s %s since %s\n", "emulator", emu.Name, ago(emu.At))
	} else {
		fmt.Printf("%-16s free\n", "emulator")
	}
	return nil
}

func whoLine(view stackView, emu *emuLock) string {
	switch {
	case view.Mode != "single":
		return fmt.Sprintf("%-16s parallel: every lane runs its own stack", view.Repo)
	case view.Lease == nil:
		return fmt.Sprintf("%-16s free", view.Repo)
	}
	l := view.Lease
	kind := l.Kind
	if view.Stale {
		kind += " (stale)"
	}
	what := "stack"
	if emu != nil && emu.Lane == l.Lane {
		what = "stack+emulator"
	}
	return fmt.Sprintf("%-16s %s  %s  %s  since %s  %s", view.Repo, l.Name, kind, l.Reason, ago(l.Since), what)
}
