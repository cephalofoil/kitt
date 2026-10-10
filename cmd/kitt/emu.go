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
// when it has one, and with servers, its dev servers and its app in the emulator.
func focus(lane Lane, agentPane string, servers bool) []string {
	var notes []string
	if hasHerdr() {
		if _, err := herdrOpen(lane, true); err != nil {
			notes = append(notes, "herdr: "+err.Error())
		} else if agentPane != "" {
			_ = herdr(nil, "agent", "focus", agentPane)
		}
	}
	if !servers {
		return notes
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
	rest, opts := flags(args, "agent", "blank", "new-agent", "no-up", "restart")
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
	var notes []string
	cfg := loadRepoConfig(lane.Main, lane.Path)
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
			if err := herdr(nil, "agent", "prompt", pane, prompt); err != nil {
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
	notes = append(notes, focus(lane, pane, opts["no-up"] == "")...)
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
