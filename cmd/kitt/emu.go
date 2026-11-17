package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// One emulator (or simulator) is the runtime for every lane: its dev client
// loads the bundle of whichever lane's Metro it is pointed at.

// pointEmulator loads a lane's bundle into the dev client: the lane's ports are
// made to reach the device, then the client is sent to the lane's Metro.
// While the lane's proof runs, that is the device the proof began on.
func pointEmulator(lane Lane) (string, error) { return pointOn(lane, laneDevice) }

// pointOn is pointEmulator on the device `pick` finds.
func pointOn(lane Lane, pick func(Lane, RepoConfig) (device, error)) (string, error) {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	app := cfg.expo()
	if app == nil {
		return "", fail("%s has no expo app", lane.Repo)
	}
	if !lane.Managed {
		return "", fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	// A running proof owns the emulator: loading another lane over it would spoil its shots.
	var held emuLock
	if readJSON(lockPath(), &held) == nil && held.Lane != lane.key() && held.fresh() {
		return "", fail("the emulator is recording a proof for %s (since %s): wait for it, or end it with `kitt proof end`", held.Name, ago(held.At))
	}
	port := app.port(lane.Slot)
	if !listening(port) {
		return "", fail("Metro of %s is not running on %d: kitt up %s", lane.Name, port, lane.Name)
	}
	if app.Scheme == "" {
		return "", fail("no deep-link scheme known for %s: set `scheme` on the app in kitt.toml", app.Name)
	}
	dev, err := pick(lane, cfg)
	if err != nil {
		return "", err
	}

	ports := append([]int{}, cfg.Emulator.Reverse...)
	for _, other := range cfg.Apps {
		if p := other.port(lane.Slot); p > 0 {
			ports = append(ports, p)
		}
	}
	if err := dev.reach(ports); err != nil {
		return "", err
	}

	link := app.Scheme + "://expo-development-client/?url=" + url.QueryEscape("http://localhost:"+strconv.Itoa(port))
	if err := dev.open(link, app.Package); err != nil {
		return "", err
	}

	_ = updateState(func(s *State) {
		s.Emulator = &EmuState{Lane: lane.key(), Port: port, Device: dev.String(), Platform: dev.Platform, ID: dev.ID, At: time.Now()}
	})
	return fmt.Sprintf("%s → %s (Metro %d)", dev, lane.Name, port), nil
}

func cmdEmu(args []string) error {
	rest, opts := flags(args, "force")
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	if cfg.expo() == nil {
		return fail("%s has no expo app", lane.Repo)
	}
	_, notes, err := acquireStack(lane, cfg, "soft", "emu", 0, opts["force"] != "")
	if err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Println(note)
	}
	message, err := pointEmulator(lane)
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

// focusOpts says how far a focus goes.
type focusOpts struct {
	Servers bool // the lane's dev servers and its app in the emulator; --no-up turns it off
	Force   bool // breaks another lane's hard lease (single mode)
}

// focusStack takes the repo's stack for a lane being gone into (single mode):
// the lane that had it is stopped. Outside the single mode it does nothing.
func focusStack(lane Lane, cfg RepoConfig, force bool) ([]string, error) {
	if !lane.Managed {
		return nil, nil
	}
	if err := needsHerdr(cfg); err != nil {
		return nil, err
	}
	_, stopped, err := acquireStack(lane, cfg, "soft", "focus", 0, force)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, line := range stopped {
		notes = append(notes, strings.TrimSpace(line))
	}
	return notes, nil
}

// focus takes the person into a lane: its herdr workspace, its agent's pane
// when it has one, and with servers, its dev servers and its app in the emulator.
// In the single mode a focus with servers takes the stack first, stopping the
// lane that had it; an agent's test keeps it out unless Force.
func focus(lane Lane, agentPane string, o focusOpts) ([]string, error) {
	cfg := loadRepoConfig(lane.Main, lane.Path)
	var notes []string
	if o.Servers {
		var err error
		if notes, err = focusStack(lane, cfg, o.Force); err != nil {
			return nil, err
		}
	}
	if hasHerdr() {
		if _, err := herdrOpen(lane, true); err != nil {
			notes = append(notes, "herdr: "+err.Error())
		} else if agentPane != "" {
			_ = herdrNow().focusAgent(agentPane)
		}
	}
	if !o.Servers {
		return notes, nil
	}
	if lane.Managed && !lane.hasExpo(cfg) {
		// A lane without a phone app: start what it runs, nothing to load into the emulator.
		if lines, err := up(lane, nil, false); err != nil {
			notes = append(notes, "dev servers: "+err.Error())
		} else {
			notes = append(notes, strings.Join(lines, " · "))
		}
	}
	if lane.Managed && lane.hasExpo(cfg) {
		// Going into a lane means seeing its app: start what is not running and wait for Metro.
		notes = append(notes, startMetro(lane, cfg)...)
		if message, err := pointEmulator(lane); err != nil {
			notes = append(notes, "emulator: "+err.Error())
		} else {
			notes = append(notes, message)
		}
	}
	return notes, nil
}

// startMetro starts what of a lane is not running, restarts what was started
// with a config that has changed since, and waits for Metro.
func startMetro(lane Lane, cfg RepoConfig) []string {
	port := cfg.expo().port(lane.Slot)
	wasUp := listening(port)
	var notes []string
	_, restarted, err := upApps(lane, nil, false, false)
	if err != nil {
		notes = append(notes, "dev servers: "+err.Error())
	}
	if wasUp && len(restarted) == 0 {
		return notes
	}
	for waited := 0; waited < 90 && !listening(port); waited += 2 {
		time.Sleep(2 * time.Second)
	}
	if len(restarted) > 0 {
		return append(notes, "restarted dev servers: their config changed")
	}
	return append(notes, "started dev servers")
}

func cmdFocus(args []string) error {
	rest, opts := flags(args, "agent", "blank", "new-agent", "no-up", "restart", "force")
	if opts["no-up"] != "" && opts["restart"] != "" {
		// One leaves the servers as they are, the other stops and starts them.
		return fail("usage: kitt focus [lane] [--no-up | --restart]: --no-up starts nothing, --restart starts the running servers again")
	}
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	pane := ""
	for _, view := range views(false) {
		if view.key() == lane.key() {
			pane = view.AgentPane
		}
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	o := focusOpts{Servers: opts["no-up"] == "", Force: opts["force"] != ""}
	// The stack is taken first, before an agent is started or a server restarted:
	// a lane whose stack another lane's test holds goes no further. A focus
	// without servers takes no lease.
	var notes []string
	if o.Servers {
		if notes, err = focusStack(lane, cfg, o.Force); err != nil {
			return err
		}
	}
	if opts["agent"] != "" || opts["blank"] != "" || opts["new-agent"] != "" || opts["prompt"] != "" {
		if !hasHerdr() {
			return fail("an agent needs herdr")
		}
		// The person is taken there first: the agent takes a moment to start.
		_, _ = herdrOpen(lane, true)
		if opts["new-agent"] != "" {
			pane, err = anotherAgent(lane, cfg)
		} else {
			pane, err = openAgent(lane, cfg)
		}
		if err != nil {
			return err
		}
		prompt := opts["prompt"]
		if opts["blank"] == "" && opts["new-agent"] == "" {
			prompt = agentPrompt(lane, cfg, prompt)
		}
		if prompt != "" {
			if err := herdrNow().prompt(pane, prompt); err != nil {
				return err
			}
			notes = append(notes, "agent is on it")
		}
	}
	if opts["restart"] != "" {
		// What runs comes back, also an app that was started by name.
		own := func() []string {
			var names []string
			for _, app := range cfg.Apps {
				if !app.Shared && app.Dev != "" && listening(app.port(lane.Slot)) {
					names = append(names, app.Name)
				}
			}
			return names
		}
		running := own()
		down(lane)
		// A port is held a moment longer than the server that had it: what
		// still answers would be taken for running and not started again.
		for waited := 0; waited < 30 && len(own()) > 0; waited++ {
			time.Sleep(500 * time.Millisecond)
		}
		if len(running) > 0 {
			if _, err := up(lane, running, false); err != nil {
				return err
			}
			// Going into the lane starts what its Metro lacks: it waits here, so it is not started twice.
			if expo := cfg.expo(); expo != nil && strings.Contains(" "+strings.Join(running, " ")+" ", " "+expo.Name+" ") {
				for waited := 0; waited < 90 && !listening(expo.port(lane.Slot)); waited += 2 {
					time.Sleep(2 * time.Second)
				}
			}
			notes = append(notes, "restarted "+strings.Join(running, ", "))
		}
	}
	more, err := focus(lane, pane, o)
	if err != nil {
		return err
	}
	notes = append(notes, more...)
	if len(notes) == 0 {
		notes = []string{"opened " + lane.Name}
	}
	// One line: it is what the dashboard shows when this ran for a key press.
	fmt.Println(strings.Join(notes, " · "))
	return nil
}

// --- The emulator lock -------------------------------------------------------

// A proof drives the emulator for a while; the lock keeps two lanes from
// loading their bundles over each other.

type emuLock struct {
	Lane string    `json:"lane"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`
	// Holder is the session the lock lives as long as; 0 for a lock written before it was kept.
	Holder int `json:"holder,omitempty"`
}

const lockStale = 20 * time.Minute

// emuMutex guards reading and writing the emulator lock, so two lanes asking
// at the same moment cannot both read it free.
const emuMutex = "emulator-mutex"

func lockPath() string { return filepath.Join(configDir(), "emulator.lock") }

func (l emuLock) fresh() bool {
	return now().Sub(l.At) < lockStale && (l.Holder == 0 || alive(l.Holder))
}

// takeEmulator takes the emulator for a lane if it is free, held by the lane
// itself, or held by `replacing` (a lane being displaced); else it says who holds it.
func takeEmulator(lane Lane, replacing string, force bool) (*emuLock, error) {
	var busy *emuLock
	err := locked(emuMutex, func() error {
		var held emuLock
		if readJSON(lockPath(), &held) == nil && held.Lane != lane.key() && held.Lane != replacing && held.fresh() && !force {
			busy = &held
			return nil
		}
		return writeJSON(lockPath(), emuLock{Lane: lane.key(), Name: lane.Name, At: now(), Holder: holderPid()})
	})
	return busy, err
}

// acquireEmulator waits until the emulator is free, up to `wait`.
func acquireEmulator(lane Lane, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		held, err := takeEmulator(lane, "", false)
		if err != nil || held == nil {
			return err
		}
		if time.Now().After(deadline) {
			return fail("the emulator is held by %s since %s ago", held.Name, ago(held.At))
		}
		if !announced {
			fmt.Printf("waiting for the emulator: %s holds it\n", held.Name)
			announced = true
		}
		time.Sleep(poll)
	}
}

func releaseEmulator(lane Lane) { releaseEmulatorOf(lane.key()) }

// releaseEmulatorOf removes the lock if the lane with this key holds it.
func releaseEmulatorOf(key string) {
	_ = locked(emuMutex, func() error {
		var held emuLock
		if readJSON(lockPath(), &held) == nil && held.Lane == key {
			return os.Remove(lockPath())
		}
		return nil
	})
}
