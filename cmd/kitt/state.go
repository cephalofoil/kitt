package main

import (
	"os"
	"path/filepath"
	"time"
)

// State is what kitt remembers between runs: which worktrees are lanes, the
// slot each holds, and the last check and proof of each.
type State struct {
	Lanes    map[string]*LaneState `json:"lanes"`
	Emulator *EmuState             `json:"emulator,omitempty"`
}

type LaneState struct {
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Slot  int    `json:"slot"`
	Issue int    `json:"issue,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
	// Ticket is a tracker's identifier (Linear's ENG-123), Branch the branch the
	// lane was made on when it was given one, Prompt what its agent was started with.
	Ticket string `json:"ticket,omitempty"`
	Branch string `json:"branch,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// AgentKind is what the lane's agent last started as (claude, opencode), when it was chosen.
	AgentKind string    `json:"agent_kind,omitempty"`
	Created   time.Time `json:"created"`
	Setup     []string  `json:"setup,omitempty"`
	// Apps are the apps this lane is about; empty means the repo's usual ones.
	Apps []string `json:"apps,omitempty"`
	// Phone is the address of this machine the lane's phone Metro was started
	// for, PhonePane the herdr pane it runs in.
	Phone     string       `json:"phone,omitempty"`
	PhonePane string       `json:"phone_pane,omitempty"`
	Checks    *CheckResult `json:"checks,omitempty"`
	Proof     *ProofState  `json:"proof,omitempty"`
	// Dev is how each app's dev server was last started, by app name.
	Dev map[string]DevStart `json:"dev,omitempty"`
}

// DevStart is what an app's dev server was started with: the pane it runs in
// and a hash of its config, so a changed config restarts it.
type DevStart struct {
	Pane string    `json:"pane"`
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
}

type EmuState struct {
	Lane   string `json:"lane"`
	Port   int    `json:"port"`
	Device string `json:"device,omitempty"`
	// Platform and ID name the device itself, so a running proof stays on it.
	Platform string    `json:"platform,omitempty"`
	ID       string    `json:"id,omitempty"`
	At       time.Time `json:"at"`
}

type CheckResult struct {
	At     time.Time `json:"at"`
	Commit string    `json:"commit"`
	Tree   string    `json:"tree,omitempty"`
	Passed int       `json:"passed"`
	Failed []string  `json:"failed,omitempty"`
}

type ProofState struct {
	Dir     string    `json:"dir"`
	Status  string    `json:"status"`
	Shots   int       `json:"shots"`
	Commit  string    `json:"commit"`
	Tree    string    `json:"tree,omitempty"`
	Note    string    `json:"note,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
}

func statePath() string { return filepath.Join(configDir(), "state.json") }

func loadState() State {
	var s State
	_ = readJSON(statePath(), &s)
	if s.Lanes == nil {
		s.Lanes = map[string]*LaneState{}
	}
	return s
}

// updateState reads, changes and writes the state under a lock, so two kitt
// processes (a dashboard and an agent's `kitt proof`) never lose each other's write.
func updateState(change func(*State)) error {
	return locked("state", func() error {
		s := loadState()
		change(&s)
		return writeJSON(statePath(), s)
	})
}

// locked runs fn while holding the named lock in kitt's folder: a directory,
// made in one step, so of two processes exactly one gets it. A lock older than
// ten seconds is left over from a crash and is taken.
func locked(name string, fn func() error) error {
	lock := filepath.Join(configDir(), name+".lock")
	_ = os.MkdirAll(configDir(), 0o755)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := os.Mkdir(lock, 0o755); err == nil {
			break
		}
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > 10*time.Second {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return fail("kitt %s is locked by another process", name)
		}
		time.Sleep(40 * time.Millisecond)
	}
	defer os.Remove(lock)
	return fn()
}

// laneEntry is a lane's entry, made when it has none yet. The main checkout is
// a lane without an entry until something is kept for it (a check, a proof, a
// dev server); its entry has slot 0, which freeSlot never hands out, and
// lanesOf keeps it the main checkout, named by its base branch.
func (s *State) laneEntry(lane Lane) *LaneState {
	entry := s.Lanes[lane.key()]
	if entry == nil {
		entry = &LaneState{Repo: lane.Repo, Name: lane.Name, Path: lane.Path, Slot: lane.Slot, Created: time.Now()}
		s.Lanes[lane.key()] = entry
	}
	return entry
}

// freeSlot is the lowest slot above 0 no lane of the repo holds; slot 0 is the
// main checkout's.
func (s State) freeSlot(repo string) int {
	used := map[int]bool{}
	for _, lane := range s.Lanes {
		if lane.Repo == repo {
			used[lane.Slot] = true
		}
	}
	for slot := 1; ; slot++ {
		if !used[slot] {
			return slot
		}
	}
}
