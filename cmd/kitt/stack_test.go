package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stackWorld is a kitt home with one repo in the single mode and fakes for
// what the lease reaches outside itself for: stopping servers, process liveness, the clock.
type stackWorld struct {
	t       *testing.T
	cfg     RepoConfig
	mu      sync.Mutex
	stopped []string
	dead    map[int]bool
	clock   time.Time
}

func newStackWorld(t *testing.T, mode string) *stackWorld {
	t.Helper()
	t.Setenv("KITT_HOME", t.TempDir())
	w := &stackWorld{t: t, dead: map[int]bool{}, clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	w.cfg = RepoConfig{Name: "r", Stack: StackCfg{Mode: mode}, Apps: []App{
		{Name: "api", Kind: "backend", Port: 8000, Dev: "run api"},
		{Name: "web", Kind: "web", Port: 3000, Dev: "run web"},
	}}

	oldStopper, oldAlive, oldNow, oldPoll := stopper, alive, now, poll
	t.Cleanup(func() { stopper, alive, now, poll = oldStopper, oldAlive, oldNow, oldPoll })
	stopper = func(lane Lane, cfg RepoConfig) []string {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.stopped = append(w.stopped, lane.Name)
		return []string{"stopped " + lane.Name}
	}
	alive = func(pid int) bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return pid > 0 && !w.dead[pid]
	}
	now = func() time.Time {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.clock
	}
	poll = 10 * time.Millisecond
	return w
}

func (w *stackWorld) lane(name string) Lane {
	return Lane{Repo: "r", Main: "/repo/r", Path: "/repo/r-lanes/" + name, Name: name, Slot: 1, Managed: true}
}

func (w *stackWorld) advance(d time.Duration) {
	w.mu.Lock()
	w.clock = w.clock.Add(d)
	w.mu.Unlock()
}

func (w *stackWorld) stops() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.stopped...)
}

func (w *stackWorld) acquire(name, kind string, wait time.Duration, force bool) (*Lease, error) {
	displaced, _, err := acquireStack(w.lane(name), w.cfg, kind, kind+"-test", wait, force)
	return displaced, err
}

func (w *stackWorld) lease() Lease {
	w.t.Helper()
	l, ok := readLease("r")
	if !ok {
		w.t.Fatal("no lease written")
	}
	return l
}

func TestStackParallelModeDoesNothing(t *testing.T) {
	w := newStackWorld(t, "")
	if _, err := w.acquire("a", "hard", 0, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLease("r"); ok {
		t.Fatal("parallel mode wrote a lease")
	}
	if exists(leasePath("r")) {
		t.Fatal("parallel mode wrote a lease file")
	}
}

func TestStackFirstLaneGetsSoftLease(t *testing.T) {
	w := newStackWorld(t, "single")
	displaced, err := w.acquire("a", "soft", 0, false)
	if err != nil || displaced != nil {
		t.Fatalf("displaced %v, err %v", displaced, err)
	}
	l := w.lease()
	if l.Name != "a" || l.Kind != "soft" || l.Lane != w.lane("a").key() {
		t.Fatalf("lease %+v", l)
	}
}

func TestStackSoftLeaseIsTakenOver(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "soft", 0, false)
	displaced, err := w.acquire("b", "soft", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if displaced == nil || displaced.Name != "a" {
		t.Fatalf("displaced %+v", displaced)
	}
	if got := w.stops(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("stopped %v, want [a]", got)
	}
	if w.lease().Name != "b" {
		t.Fatalf("lease is %s's", w.lease().Name)
	}
}

func TestStackHardLeaseKeepsOthersOut(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	_, err := w.acquire("b", "soft", 0, false)
	if err == nil || !strings.Contains(err.Error(), "a (hard-test") {
		t.Fatalf("err %v, want one naming a and its reason", err)
	}
	if len(w.stops()) != 0 {
		t.Fatalf("stopped %v", w.stops())
	}
	if w.lease().Name != "a" {
		t.Fatal("the lease changed hands")
	}
}

func TestStackHardWaitsForRelease(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)

	start := time.Now()
	if _, err := w.acquire("b", "hard", 100*time.Millisecond, false); err == nil {
		t.Fatal("b got a held stack")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("b did not wait")
	}

	done := make(chan error)
	go func() {
		_, err := w.acquire("b", "hard", 5*time.Second, false)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := releaseStack(w.lane("a"), false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("b after the release: %v", err)
	}
	if l := w.lease(); l.Name != "b" || l.Kind != "hard" {
		t.Fatalf("lease %+v", l)
	}
}

func TestStackForceDisplacesHard(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	displaced, err := w.acquire("b", "soft", 0, true)
	if err != nil || displaced == nil || displaced.Name != "a" {
		t.Fatalf("displaced %+v, err %v", displaced, err)
	}
	if got := w.stops(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("stopped %v", got)
	}
}

func TestStackDeadHolderIsStale(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	w.dead[w.lease().Holder] = true
	if _, err := w.acquire("b", "soft", 0, false); err != nil {
		t.Fatal(err)
	}
	if w.lease().Name != "b" {
		t.Fatal("b did not get the stale lease")
	}
}

func TestStackQuietHolderIsStale(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	w.advance(21 * time.Minute)
	if _, err := w.acquire("b", "soft", 0, false); err != nil {
		t.Fatal(err)
	}

	// A ttl of the repo's own.
	w.cfg.Stack.LeaseTTL = "1m"
	_, _ = w.acquire("a", "hard", 0, true)
	w.advance(30 * time.Second)
	if _, err := w.acquire("b", "soft", 0, false); err == nil {
		t.Fatal("b took a lease renewed within the ttl")
	}
	renewStack(w.lane("a"))
	w.advance(45 * time.Second)
	if _, err := w.acquire("b", "soft", 0, false); err == nil {
		t.Fatal("the renewal did not count")
	}
	w.advance(30 * time.Second)
	if _, err := w.acquire("b", "soft", 0, false); err != nil {
		t.Fatal(err)
	}
}

// Two agents asking at the same moment: exactly one gets the stack.
func TestStackHardRace(t *testing.T) {
	w := newStackWorld(t, "single")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = w.acquire(string(rune('a'+i)), "hard", 0, false)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d lanes got the stack: %v", won, errs)
	}
}

func TestEmulatorRace(t *testing.T) {
	w := newStackWorld(t, "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			busy, err := takeEmulator(w.lane(string(rune('a'+i))), "", false)
			if err == nil && busy == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d lanes got the emulator", won)
	}
}

func TestStackRelease(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	if _, err := releaseStack(w.lane("a"), false); err != nil {
		t.Fatal(err)
	}
	if l := w.lease(); l.Kind != "soft" || l.Name != "a" {
		t.Fatalf("after release: %+v", l)
	}
	if len(w.stops()) != 0 {
		t.Fatal("a release without --down stopped servers")
	}

	if _, err := releaseStack(w.lane("a"), true); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLease("r"); ok {
		t.Fatal("release --down kept the lease")
	}
	if got := w.stops(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("stopped %v", got)
	}
}

// The holder taking its own stack softly (kitt up during its proof) keeps it hard.
func TestStackHolderStaysHard(t *testing.T) {
	w := newStackWorld(t, "single")
	_, _ = w.acquire("a", "hard", 0, false)
	if _, err := w.acquire("a", "soft", 0, false); err != nil {
		t.Fatal(err)
	}
	if l := w.lease(); l.Kind != "hard" || l.Reason != "hard-test" {
		t.Fatalf("lease %+v", l)
	}
}

func TestStackAppsLeaveSharedAndKept(t *testing.T) {
	cfg := RepoConfig{Stack: StackCfg{Mode: "single", Keep: []string{"db"}}, Apps: []App{
		{Name: "api"}, {Name: "db"}, {Name: "inngest", Shared: true}, {Name: "web", Lazy: true},
	}}
	var names []string
	for _, app := range stackApps(cfg) {
		names = append(names, app.Name)
	}
	if strings.Join(names, ",") != "api,web" {
		t.Fatalf("stack apps %v, want api,web", names)
	}
}

func TestWhoJSON(t *testing.T) {
	w := newStackWorld(t, "single")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "kitt.toml"), []byte("name = \"r\"\nbase = \"main\"\n[stack]\nmode = \"single\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveGlobal(Global{Repos: []RepoRef{{Name: "r", Path: repo}}}); err != nil {
		t.Fatal(err)
	}
	_, _ = w.acquire("a", "hard", 0, false)

	out := captureStdout(t, func() {
		if err := cmdWho([]string{"--json"}); err != nil {
			t.Fatal(err)
		}
	})
	var got struct {
		Stacks []struct {
			Repo  string `json:"repo"`
			Mode  string `json:"mode"`
			Stale bool   `json:"stale"`
			Lease *struct {
				Name   string `json:"name"`
				Kind   string `json:"kind"`
				Reason string `json:"reason"`
				Since  string `json:"since"`
			} `json:"lease"`
		} `json:"stacks"`
		Emulator *json.RawMessage `json:"emulator"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(got.Stacks) != 1 || got.Stacks[0].Repo != "r" || got.Stacks[0].Mode != "single" || got.Stacks[0].Stale {
		t.Fatalf("stacks %s", out)
	}
	if l := got.Stacks[0].Lease; l == nil || l.Name != "a" || l.Kind != "hard" || l.Reason != "hard-test" || l.Since == "" {
		t.Fatalf("lease %s", out)
	}
	if !strings.Contains(out, `"emulator":null`) {
		t.Fatalf("emulator: %s", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = wr
	fn()
	wr.Close()
	os.Stdout = old
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	return b.String()
}
