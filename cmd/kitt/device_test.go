package main

import (
	"strings"
	"testing"
	"time"
)

// fakeDevices stands in for adb and simctl: the devices each platform lists.
func fakeDevices(t *testing.T, mac bool, devices map[string][]device) {
	t.Helper()
	t.Setenv("KITT_PLATFORM", "")
	oldList, oldMac := listDevices, onMac
	t.Cleanup(func() { listDevices, onMac = oldList, oldMac })
	onMac = mac
	listDevices = func(platform string) ([]device, error) {
		if platform == "ios" && !mac {
			return nil, fail("no simctl here")
		}
		return devices[platform], nil
	}
}

var (
	pixel  = device{Platform: "android", ID: "emulator-5554", Name: "emulator-5554"}
	iphone = device{Platform: "ios", ID: "U-16", Name: "iPhone 16"}
	ipad   = device{Platform: "ios", ID: "U-PAD", Name: "iPad", Off: true}
)

func TestPickDevice(t *testing.T) {
	ios := RepoConfig{Emulator: EmuCfg{Platform: "ios"}}
	for _, c := range []struct {
		what    string
		mac     bool
		devices map[string][]device
		cfg     RepoConfig
		env     string
		want    string
		err     string
	}{
		{what: "auto: Android wins", mac: true, devices: map[string][]device{"android": {pixel}, "ios": {iphone}}, want: "emulator-5554"},
		{what: "auto: the simulator without Android", mac: true, devices: map[string][]device{"ios": {ipad, iphone}}, want: "U-16"},
		{what: "auto off a Mac: no simulator asked", devices: map[string][]device{}, err: "no Android"},
		{what: "kitt.toml asks for iOS", mac: true, devices: map[string][]device{"android": {pixel}, "ios": {iphone}}, cfg: ios, want: "U-16"},
		{what: "KITT_PLATFORM asks for iOS", mac: true, devices: map[string][]device{"android": {pixel}, "ios": {iphone}}, env: "ios", want: "U-16"},
		{what: "a named simulator, by name", mac: true, devices: map[string][]device{"android": {pixel}, "ios": {iphone}},
			cfg: RepoConfig{Emulator: EmuCfg{Simulator: "iPhone 16"}}, want: "U-16"},
		{what: "a named simulator that is not booted", mac: true, devices: map[string][]device{"ios": {ipad, iphone}},
			cfg: RepoConfig{Emulator: EmuCfg{Simulator: "U-PAD"}}, err: "not booted"},
		{what: "iOS off a Mac", devices: map[string][]device{"android": {pixel}}, cfg: ios, err: "needs a Mac"},
	} {
		t.Run(c.what, func(t *testing.T) {
			fakeDevices(t, c.mac, c.devices)
			t.Setenv("KITT_PLATFORM", c.env)
			dev, err := pickDevice(c.cfg)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("got %v, %v; want an error with %q", dev, err, c.err)
				}
				return
			}
			if err != nil || dev.ID != c.want {
				t.Fatalf("got %v, %v; want %s", dev, err, c.want)
			}
		})
	}
}

// A running proof stays on the device it began on: an Android emulator
// started meanwhile does not take its shots, and a device gone says so.
func TestProofKeepsItsDevice(t *testing.T) {
	t.Setenv("KITT_HOME", t.TempDir())
	lane := Lane{Repo: "r", Name: "a", Path: "/repo/r-lanes/a", Main: "/repo/r", Slot: 1, Managed: true}
	setState := func(proof string, emu *EmuState) {
		t.Helper()
		if err := updateState(func(s *State) {
			s.Lanes[lane.key()] = &LaneState{Repo: "r", Name: "a", Path: lane.Path, Slot: 1, Proof: &ProofState{Status: proof}}
			s.Emulator = emu
		}); err != nil {
			t.Fatal(err)
		}
	}
	onIPhone := &EmuState{Lane: lane.key(), Device: iphone.String(), Platform: "ios", ID: iphone.ID, At: time.Now()}

	fakeDevices(t, true, map[string][]device{"android": {pixel}, "ios": {iphone}})
	setState("running", onIPhone)
	if dev, err := laneDevice(lane, RepoConfig{}); err != nil || dev.ID != iphone.ID {
		t.Fatalf("during the proof: %v, %v; want the simulator it began on", dev, err)
	}

	// The simulator is shut down: no silent switch to the Android emulator.
	fakeDevices(t, true, map[string][]device{"android": {pixel}, "ios": {{Platform: "ios", ID: iphone.ID, Name: iphone.Name, Off: true}}})
	if dev, err := laneDevice(lane, RepoConfig{}); err == nil || !strings.Contains(err.Error(), "iPhone 16") || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("with its device gone: %v, %v", dev, err)
	}

	// Outside a proof, and with a state from before the device was kept, the device is picked.
	setState("pass", onIPhone)
	if dev, err := laneDevice(lane, RepoConfig{}); err != nil || dev.ID != pixel.ID {
		t.Fatalf("after the proof: %v, %v", dev, err)
	}
	setState("running", &EmuState{Lane: lane.key(), Device: "Android emulator-5554", At: time.Now()})
	if dev, err := laneDevice(lane, RepoConfig{}); err != nil || dev.ID != pixel.ID {
		t.Fatalf("an old state: %v, %v", dev, err)
	}
	// The emulator pointed at another lane is no record of this one's device.
	setState("running", &EmuState{Lane: "/repo/r-lanes/b", Platform: "ios", ID: "U-GONE", At: time.Now()})
	if dev, err := laneDevice(lane, RepoConfig{}); err != nil || dev.ID != pixel.ID {
		t.Fatalf("another lane's device: %v, %v", dev, err)
	}
}
