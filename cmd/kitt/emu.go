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
func pointEmulator(lane Lane) (string, error) {
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
	dev, err := pickDevice(cfg)
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
		s.Emulator = &EmuState{Lane: lane.key(), Port: port, Device: dev.String(), At: time.Now()}
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

// focus takes the person into a lane: its herdr workspace, its agent's pane
// when it has one, and its app in the emulator. In the single mode it takes the
// stack first, stopping the lane that had it; an agent's test keeps it out
// unless `force`.
func focus(lane Lane, agentPane string, force bool) ([]string, error) {
	var notes []string
	cfg := loadRepoConfig(lane.Main, lane.Path)
	if lane.Managed {
		if err := needsHerdr(cfg); err != nil {
			return nil, err
		}
		_, stopped, err := acquireStack(lane, cfg, "soft", "focus", 0, force)
		if err != nil {
			return nil, err
		}
		for _, line := range stopped {
			notes = append(notes, strings.TrimSpace(line))
		}
	}
	if hasHerdr() {
		if _, err := herdrOpen(lane, true); err != nil {
			notes = append(notes, "herdr: "+err.Error())
		} else if agentPane != "" {
			_ = herdr(nil, "agent", "focus", agentPane)
		}
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
	lines, err := up(lane, nil, false)
	if err != nil {
		notes = append(notes, "dev servers: "+err.Error())
	}
	restarted := false
	for _, line := range lines {
		restarted = restarted || strings.HasSuffix(line, "restarting")
	}
	if wasUp && !restarted {
		return notes
	}
	for waited := 0; waited < 90 && !listening(port); waited += 2 {
		time.Sleep(2 * time.Second)
	}
	if restarted {
		return append(notes, "restarted dev servers: their config changed")
	}
	return append(notes, "started dev servers")
}

func cmdFocus(args []string) error {
	rest, opts := flags(args, "force")
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
	notes, err := focus(lane, pane, opts["force"] != "")
	if err != nil {
		return err
	}
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
