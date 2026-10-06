// kitt runs parallel work on a repo as lanes: one worktree per ticket, each with
// its own ports, its own agent in herdr, and a proof that the change works.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

const usage = `kitt — lanes for parallel work

  kitt dash [--workspace]        the dashboard (--workspace opens it as a herdr workspace)
  kitt new <issue|ticket|name>   a new lane: worktree, branch, linked env files, setup
        [--prompt t] [--no-agent]  an issue lane starts an agent on the issue; --prompt gives any lane one
        [--prompt-env VAR]         the prompt from an environment variable (Linear's LINEAR_PROMPT)
        [--branch b] [--dir d]     the branch to use instead of branch_prefix + name · the repo by a directory
        [--apps web,admin]         the apps the lane is about, when not the repo's usual ones
        [--repo r] [--base ref] [--focus] [--no-setup]
  kitt pr <number>               check an open pull request out as a lane, to look at it before the merge
  kitt agent [lane] [--prompt t] start the lane's agent, or hand the running one a prompt
  kitt ls [--all] [--json]       the lanes
  kitt focus [lane] [--force]    go into a lane: its herdr workspace, its dev servers, its app in the emulator
  kitt up [lane] [app...]        start the lane's dev servers on its own ports
  kitt down [lane]               stop them
  kitt emu [lane] [--force]      point the emulator at the lane's Metro
  kitt claim [lane]              take the stack for a test (single mode) and start the lane's servers
        [--wait 10m] [--reason t]  wait for a lane holding it · say what for
  kitt release [lane] [--down]   hand the stack back; --down also stops the servers
  kitt who [--json]              which lane holds each repo's stack, and the emulator
  kitt open [lane] [app]         open the lane's web app in the browser
  kitt env [lane]                the lane's ports
  kitt check [lane] [--all]      run the repo's checks for the apps the lane touched
  kitt proof begin [--wait 10m]  take the emulator (and the stack in single mode), load the lane's app
  kitt proof shot <label>        a screenshot into the proof
  kitt proof add <file> [label]  any other image into the proof
  kitt proof end --pass|--fail   release the emulator and the stack, write the proof page
  kitt proof open [lane]         look at a lane's proof
  kitt adopt [lane|path]         make an existing worktree a lane
  kitt link [lane]               link the env files again
  kitt setup [lane]              install what the lane runs and is not installed yet
  kitt rm <lane> [--force]       remove a lane that holds nothing unsaved
  kitt repo add [path] | list | rm <name>
  kitt init [path]               set a repo up: what kitt detects, shown piece by piece to keep, edit or drop
        [--agent]                  an agent reads the repo first and fills in what detection cannot see
        [--home] [--yes]           write into kitt's folder, not the repo · take everything without asking
  kitt detect [path]             print what kitt detects in a repo; registers and writes nothing
  kitt doctor                    what kitt needs and whether it is there
  kitt logo                      the dashboard's logo in its variants (KITT_LOGO picks one)

The emulator is an Android emulator through adb or, on a Mac, a booted iOS simulator;
kitt picks the one running (Android first). KITT_PLATFORM=android|ios, or platform in
kitt.toml's [emulator], chooses.

[stack] mode = "single" in kitt.toml runs one lane's servers at a time: focus and up take
the stack over and stop the lane that had it; a proof or a claim holds it until released.

A lane is named by its name, <repo>/<name>, its issue number or ticket, or nothing at all
inside its directory.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}
	commands := map[string]func([]string) error{
		"dash": cmdDash, "new": cmdNew, "ls": cmdLs, "list": cmdLs, "focus": cmdFocus, "up": cmdUp, "down": cmdDown,
		"emu": cmdEmu, "env": cmdEnv, "check": cmdCheck, "proof": cmdProof, "adopt": cmdAdopt, "link": cmdLink,
		"rm": cmdRm, "agent": cmdAgent, "logo": cmdLogo, "setup": cmdSetup, "pr": cmdPr, "open": cmdOpen, "detect": cmdDetect, "repo": cmdRepo, "init": cmdInit, "doctor": cmdDoctor,
		"claim": cmdClaim, "release": cmdRelease, "who": cmdWho,
	}
	command, ok := commands[os.Args[1]]
	if !ok {
		if os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
			fmt.Print(usage)
			return
		}
		fmt.Fprintf(os.Stderr, "kitt: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err := command(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "kitt:", err)
		os.Exit(1)
	}
}

func cmdDoctor([]string) error {
	line := func(ok bool, name, detail string) {
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		fmt.Printf("%s %-9s %s\n", mark, name, detail)
	}
	for _, tool := range []struct{ name, why string }{
		{"git", "worktrees"}, {"gh", "issues and pull requests"}, {"herdr", "workspaces and agents"},
	} {
		path, err := exec.LookPath(tool.name)
		line(err == nil, tool.name, tool.why+"  "+path)
	}
	// The phone app runs on an Android emulator through adb or, on a Mac, an iOS
	// simulator through simctl; one of them is enough.
	adb, adbErr := exec.LookPath("adb")
	if runtime.GOOS == "darwin" {
		xcrun, simErr := exec.LookPath("xcrun")
		line(adbErr == nil || simErr == nil, "adb", "Android emulator  "+orText(adb, "not installed (optional with the iOS simulator)"))
		line(simErr == nil, "simctl", "iOS simulator  "+orText(xcrun, "install Xcode"))
	} else {
		line(adbErr == nil, "adb", "the emulator  "+adb)
	}
	if dev, err := pickDevice(RepoConfig{}); err == nil {
		line(true, "device", dev.String()+"  (KITT_PLATFORM=android|ios to choose)")
	} else {
		line(false, "device", err.Error())
	}
	line(hasHerdr(), "herdr up", "the herdr server answers")

	dir, err := os.MkdirTemp("", "kitt-doctor")
	if err == nil {
		defer os.RemoveAll(dir)
		target := dir + string(os.PathSeparator) + "a"
		_ = os.WriteFile(target, []byte("x"), 0o600)
		err = os.Symlink(target, dir+string(os.PathSeparator)+"b")
	}
	line(err == nil, "symlinks", "env files are linked into lanes (Windows: Developer Mode)")

	repos := loadGlobal().Repos
	line(len(repos) > 0, "repos", fmt.Sprintf("%d registered  (%s)", len(repos), configDir()))
	return nil
}

func orText(value, otherwise string) string {
	if value == "" {
		return otherwise
	}
	return value
}
