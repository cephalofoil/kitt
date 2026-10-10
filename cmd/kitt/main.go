// kitt runs parallel work on a repo as lanes: one worktree per ticket, each with
// its own ports, its own agent in herdr, and a proof that the change works.
package main

import (
	"fmt"
	"os"
	"os/exec"
)

const usage = `kitt — lanes for parallel work

  kitt dash [--workspace]        the dashboard (--workspace opens it as a herdr workspace)
  kitt new <issue|name>          a new lane: worktree, branch, linked env files, setup
        [--prompt t] [--no-agent]  an issue lane starts an agent on the issue; --prompt gives any lane one
        [--blank] [--up]           a blank agent, whatever the lane is for · its dev servers started
        [--apps web,admin]         the apps the lane is about, when not the repo's usual ones
        [--repo r] [--base ref] [--focus] [--no-setup]
  kitt pr <number>               check an open pull request out as a lane, to look at it before the merge
  kitt agent [lane] [--prompt t] start the lane's agent, or hand the running one a prompt
  kitt ls [--all] [--json]       the lanes
  kitt backlog [--repo r] [--json]  the repo's open issues, and which of them has a lane
  kitt focus [lane]              go into a lane: its herdr workspace, its dev servers, its app in the emulator
        [--blank | --agent | --prompt t | --new-agent]  with a blank agent, one on its issue, one told t, or one more
        [--no-up | --restart]      without the servers · with them stopped and started again
  kitt up [lane] [app...]        start the lane's dev servers on its own ports
  kitt down [lane]               stop them
  kitt emu [lane]                point the emulator at the lane's Metro
  kitt phone [lane] [--host ip]  a second Metro for a phone on the Wi-Fi, told this machine's address; prints the code to scan
  kitt open [lane] [app]         open the lane's web app in the browser
  kitt env [lane]                the lane's ports
  kitt check [lane] [--all]      run the repo's checks for the apps the lane touched
  kitt proof begin               take the emulator, load the lane's app
  kitt proof shot <label>        a screenshot into the proof
  kitt proof add <file> [label]  any other image into the proof
  kitt proof end --pass|--fail   release the emulator, write the proof page
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

A lane is named by its name, <repo>/<name>, its issue number, or nothing at all
inside its directory.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return
	}
	commands := map[string]func([]string) error{
		"dash": cmdDash, "new": cmdNew, "backlog": cmdBacklog, "ls": cmdLs, "list": cmdLs, "focus": cmdFocus, "up": cmdUp, "down": cmdDown,
		"emu": cmdEmu, "phone": cmdPhone, "env": cmdEnv, "check": cmdCheck, "proof": cmdProof, "adopt": cmdAdopt, "link": cmdLink,
		"rm": cmdRm, "agent": cmdAgent, "logo": cmdLogo, "setup": cmdSetup, "pr": cmdPr, "open": cmdOpen, "detect": cmdDetect, "repo": cmdRepo, "init": cmdInit, "doctor": cmdDoctor,
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
		{"git", "worktrees"}, {"gh", "issues and pull requests"}, {"herdr", "workspaces and agents"}, {"adb", "the emulator"},
	} {
		path, err := exec.LookPath(tool.name)
		line(err == nil, tool.name, tool.why+"  "+path)
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
