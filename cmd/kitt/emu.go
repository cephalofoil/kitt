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

// One emulator is the runtime for every lane: its dev client loads the bundle
// of whichever lane's Metro it is pointed at.

func adbDevice(cfg RepoConfig) (string, error) {
	if cfg.Emulator.Serial != "" {
		return cfg.Emulator.Serial, nil
	}
	out, err := run("", "adb", "devices")
	if err != nil {
		return "", fail("adb is not available")
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "device" {
			return fields[0], nil
		}
	}
	return "", fail("no emulator or device is attached")
}

// pointEmulator loads a lane's bundle into the dev client: the lane's ports are
// reversed into the device, then the client is sent to the lane's Metro.
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
	if readJSON(lockPath(), &held) == nil && held.Lane != lane.key() && time.Since(held.At) < lockStale {
		return "", fail("the emulator is recording a proof for %s (since %s): wait for it, or end it with `kitt proof end`", held.Name, ago(held.At))
	}
	port := app.port(lane.Slot)
	if !listening(port) {
		return "", fail("Metro of %s is not running on %d: kitt up %s", lane.Name, port, lane.Name)
	}
	if app.Scheme == "" {
		return "", fail("no deep-link scheme known for %s: set `scheme` on the app in kitt.toml", app.Name)
	}
	serial, err := adbDevice(cfg)
	if err != nil {
		return "", err
	}

	ports := append([]int{}, cfg.Emulator.Reverse...)
	for _, other := range cfg.Apps {
		if p := other.port(lane.Slot); p > 0 {
			ports = append(ports, p)
		}
	}
	for _, p := range ports {
		spec := "tcp:" + strconv.Itoa(p)
		if _, err := run("", "adb", "-s", serial, "reverse", spec, spec); err != nil {
			return "", err
		}
	}

	link := app.Scheme + "://expo-development-client/?url=" + url.QueryEscape("http://localhost:"+strconv.Itoa(port))
	start := []string{"-s", serial, "shell", "am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", "'" + link + "'"}
	if app.Package != "" {
		start = append(start, app.Package)
	}
	if out, err := runTimeout("", 30*time.Second, "adb", start...); err != nil || strings.Contains(out, "Error:") {
		return "", fail("the dev client did not open %s: %s", link, firstLine(out+" "+fmt.Sprint(err)))
	}

	_ = updateState(func(s *State) { s.Emulator = &EmuState{Lane: lane.key(), Port: port, At: time.Now()} })
	return fmt.Sprintf("emulator → %s (Metro %d)", lane.Name, port), nil
}

func cmdEmu(args []string) error {
	rest, _ := flags(args)
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	message, err := pointEmulator(lane)
	if err != nil {
		return err
	}
	fmt.Println(message)
	return nil
}

// focus takes the person into a lane: its herdr workspace, its agent's pane
// when it has one, and its app in the emulator.
func focus(lane Lane, agentPane string) []string {
	var notes []string
	if hasHerdr() {
		if _, err := herdrOpen(lane, true); err != nil {
			notes = append(notes, "herdr: "+err.Error())
		} else if agentPane != "" {
			_ = herdr(nil, "agent", "focus", agentPane)
		}
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
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
		if port := cfg.expo().port(lane.Slot); !listening(port) {
			if _, err := up(lane, nil, false); err != nil {
				notes = append(notes, "dev servers: "+err.Error())
			}
			for waited := 0; waited < 90 && !listening(port); waited += 2 {
				time.Sleep(2 * time.Second)
			}
			notes = append(notes, "started dev servers")
		}
		if message, err := pointEmulator(lane); err != nil {
			notes = append(notes, "emulator: "+err.Error())
		} else {
			notes = append(notes, message)
		}
	}
	return notes
}

func cmdFocus(args []string) error {
	rest, _ := flags(args)
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
	notes := focus(lane, pane)
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
}

const lockStale = 20 * time.Minute

func lockPath() string { return filepath.Join(configDir(), "emulator.lock") }

// acquireEmulator waits until the emulator is free, up to `wait`.
func acquireEmulator(lane Lane, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		var held emuLock
		err := readJSON(lockPath(), &held)
		free := err != nil || held.Lane == lane.key() || time.Since(held.At) > lockStale
		if free {
			return writeJSON(lockPath(), emuLock{Lane: lane.key(), Name: lane.Name, At: time.Now()})
		}
		if time.Now().After(deadline) {
			return fail("the emulator is held by %s since %s ago", held.Name, ago(held.At))
		}
		if !announced {
			fmt.Printf("waiting for the emulator: %s holds it\n", held.Name)
			announced = true
		}
		time.Sleep(3 * time.Second)
	}
}

func releaseEmulator(lane Lane) {
	var held emuLock
	if readJSON(lockPath(), &held) == nil && held.Lane == lane.key() {
		_ = os.Remove(lockPath())
	}
}
