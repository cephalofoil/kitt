package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// A device is where a lane's app runs: an Android emulator (or phone) through
// adb, or, on a Mac, an iOS simulator through simctl. Both load the same dev
// client deep link; they differ in how ports reach the app and how a shot is taken.

type device struct {
	Platform string // android | ios
	ID       string // adb serial or simulator UDID
	Name     string
}

func (d device) String() string {
	if d.Platform == "ios" {
		return "iOS simulator " + d.Name
	}
	return "Android " + d.ID
}

// platformWanted is the platform asked for: KITT_PLATFORM, else kitt.toml, else auto.
func platformWanted(cfg RepoConfig) string {
	want := strings.ToLower(strings.TrimSpace(os.Getenv("KITT_PLATFORM")))
	if want == "" {
		want = strings.ToLower(strings.TrimSpace(cfg.Emulator.Platform))
	}
	if want == "" {
		want = "auto"
	}
	return want
}

// pickDevice finds the device to use. Asked for a platform, it uses that one or
// says why it cannot; on auto, an attached Android device wins (the emulator was
// started on purpose), then a booted iOS simulator on a Mac.
func pickDevice(cfg RepoConfig) (device, error) {
	switch want := platformWanted(cfg); want {
	case "android":
		return androidDevice(cfg)
	case "ios":
		return iosDevice(cfg)
	case "auto":
	default:
		return device{}, fail("unknown platform %q: android, ios or auto (kitt.toml [emulator] platform, or KITT_PLATFORM)", want)
	}

	if cfg.Emulator.Serial != "" {
		return androidDevice(cfg)
	}
	if cfg.Emulator.Simulator != "" {
		return iosDevice(cfg)
	}
	android, androidErr := androidDevice(cfg)
	if androidErr == nil {
		return android, nil
	}
	if runtime.GOOS != "darwin" {
		return device{}, androidErr
	}
	ios, iosErr := iosDevice(cfg)
	if iosErr == nil {
		return ios, nil
	}
	return device{}, fail("no device to use: %s; %s", androidErr, iosErr)
}

func androidDevice(cfg RepoConfig) (device, error) {
	if cfg.Emulator.Serial != "" {
		return device{Platform: "android", ID: cfg.Emulator.Serial, Name: cfg.Emulator.Serial}, nil
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return device{}, fail("adb is not installed")
	}
	out, err := run("", "adb", "devices")
	if err != nil {
		return device{}, fail("adb does not answer: %s", err)
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "device" {
			return device{Platform: "android", ID: fields[0], Name: fields[0]}, nil
		}
	}
	return device{}, fail("no Android emulator or device is attached")
}

// iosDevice is the simulator named in kitt.toml (UDID or name), else the first booted one.
func iosDevice(cfg RepoConfig) (device, error) {
	if runtime.GOOS != "darwin" {
		return device{}, fail("the iOS simulator needs a Mac")
	}
	out, err := run("", "xcrun", "simctl", "list", "devices", "-j")
	if err != nil {
		return device{}, fail("simctl is not available (install Xcode): %s", err)
	}
	var list struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return device{}, fail("simctl list: %s", err)
	}
	want := cfg.Emulator.Simulator
	for _, sims := range list.Devices {
		for _, sim := range sims {
			if want != "" && sim.UDID != want && sim.Name != want {
				continue
			}
			if sim.State != "Booted" {
				if want != "" {
					return device{}, fail("the simulator %s is not booted: xcrun simctl boot %q && open -a Simulator", sim.Name, sim.UDID)
				}
				continue
			}
			return device{Platform: "ios", ID: sim.UDID, Name: sim.Name}, nil
		}
	}
	if want != "" {
		return device{}, fail("no simulator %q: xcrun simctl list devices", want)
	}
	return device{}, fail("no iOS simulator is booted: open -a Simulator")
}

// reach makes the lane's ports answer on the device's localhost. A simulator
// shares the Mac's network, so only Android needs them reversed.
func (d device) reach(ports []int) error {
	if d.Platform != "android" {
		return nil
	}
	for _, p := range ports {
		spec := "tcp:" + strconv.Itoa(p)
		if _, err := run("", "adb", "-s", d.ID, "reverse", spec, spec); err != nil {
			return err
		}
	}
	return nil
}

// open sends the dev client to a deep link.
func (d device) open(link, androidPackage string) error {
	if d.Platform == "ios" {
		if _, err := runTimeout("", 30*time.Second, "xcrun", "simctl", "openurl", d.ID, link); err != nil {
			return fail("the dev client did not open %s: %s (is it installed on %s?)", link, err, d.Name)
		}
		return nil
	}
	start := []string{"-s", d.ID, "shell", "am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", "'" + link + "'"}
	if androidPackage != "" {
		start = append(start, androidPackage)
	}
	if out, err := runTimeout("", 30*time.Second, "adb", start...); err != nil || strings.Contains(out, "Error:") {
		return fail("the dev client did not open %s: %s", link, firstLine(out+" "+errText(err)))
	}
	return nil
}

// screenshot is a PNG of the device's screen.
func (d device) screenshot() ([]byte, error) {
	if d.Platform == "ios" {
		file, err := os.CreateTemp("", "kitt-shot-*.png")
		if err != nil {
			return nil, err
		}
		path := file.Name()
		file.Close()
		defer os.Remove(path)
		if _, err := runTimeout("", 20*time.Second, "xcrun", "simctl", "io", d.ID, "screenshot", "--type=png", path); err != nil {
			return nil, err
		}
		return os.ReadFile(path)
	}
	return runBytes(20*time.Second, "adb", "-s", d.ID, "exec-out", "screencap", "-p")
}

// driveHint says how an agent works the app on this device.
func (d device) driveHint() string {
	if d.Platform == "ios" {
		return "drive it with axe or idb when installed (tap, type, describe-ui), or open screens by deep link: xcrun simctl openurl " + d.ID + " <scheme>://<path>"
	}
	return "drive it with adb: input tap / swipe / text, uiautomator dump for coordinates"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
