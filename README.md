# kitt

<img width="2172" height="724" alt="Kitt-Logo mit kobaltblauen Ts" src="https://github.com/user-attachments/assets/0bbde4f9-62bc-4e15-ac01-c3796f27b836" />


Parallel work on a repo as **lanes**: one worktree per ticket, each with its own ports, its own agent in [herdr](https://herdr.dev), and a proof that the change works in the app.

Three parts:

- `kitt`, a Go CLI: lanes, dev servers, the emulator, checks, proofs, and a dashboard.
- `lanes`, a Claude Code skill: tells an agent how to work in a lane.
- `pr-watch`, a Claude Code mod: PR checks, uncommitted work and rebase state above the prompt.

## Install

```
go build -o ~/.local/bin/kitt ./cmd/kitt     # any directory on PATH; on Windows see below

/plugin marketplace add cephalofoil/kitt
/plugin install pr-watch@kitt
/plugin install lanes@kitt
```

`kitt doctor` says what is missing (git, gh, herdr, adb, symlinks).

### On Windows

In PowerShell, from the repo root (`~` is not expanded for `go build`, so use `$HOME`):

```powershell
New-Item -ItemType Directory -Force "$HOME\.local\bin" | Out-Null
go build -o "$HOME\.local\bin\kitt.exe" ./cmd/kitt
```

Once, put that folder on your user PATH, then open a new terminal:

```powershell
$bin  = "$HOME\.local\bin"
$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ';') -notcontains $bin) {
  [Environment]::SetEnvironmentVariable("Path", "$path;$bin", "User")
}
```

To update, run the `go build` line again. `kitt doctor` checks symlinks: turn on Developer Mode (Settings → System → For developers) so env files are symlinked into lanes; without it kitt falls back to hard links.

## A day with it

```
kitt repo add                 # once, inside the repo: registers it and shows what kitt detects
kitt init                     # optional: write that as kitt.toml to edit (--home for a repo that is not yours)
kitt dash --workspace         # the dashboard as its own herdr workspace

kitt new 338                  # lane for issue 338: worktree, branch, env links, install, and an agent started on the issue
kitt new 412 --apps web       # a lane about the website: starts the web app, leaves the emulator alone
kitt new cook-mode-proto --prompt "/wayfinder cook mode across several recipes"
```

In the dashboard, one row per lane: the apps it has changed files of, the agent's state, uncommitted and behind counts, the PR and its checks, the last proof, which dev servers answer, and which lane the emulator shows.

| Key | |
|---|---|
| `enter` | go into the lane: its herdr workspace and agent, its dev servers started, its app in the emulator |
| `g` | start the lane's agent (on its issue, if it has one) |
| `e` | only point the emulator at the lane |
| `o` | open the lane's web app in the browser |
| `u` / `d` | start / stop the lane's dev servers |
| `c` | run the repo's checks for the apps the lane touched |
| `p` | open the lane's proof |
| `n` | new lane: an issue number starts an agent on it, a name only makes the lane |
| `a` | adopt a worktree kitt did not create (Claude's, herdr's, a hand-made one) |
| `x` | remove a lane that holds nothing unsaved |
| `t` | show the worktrees that are not lanes |

Under the lanes, the open pull requests that have no lane yet (a bot's are left out). `enter` on one fetches its branch and checks it out as a lane about the apps it touches; `o` shows it on GitHub. The same from the shell: `kitt pr 380`.

Every key is also a command: `kitt focus`, `kitt agent`, `kitt emu`, `kitt up`, `kitt check`, `kitt proof open`, `kitt adopt`, `kitt rm`. See `kitt help`.

## From Linear

Linear's **Work on issue → Custom script** hands an issue to kitt: a lane on Linear's branch name, an agent started on Linear's prompt, the lane's workspace shown in herdr. Opened a second time, the issue goes back to its lane; a branch that exists only on origin is checked out, not started anew.

```
ln -s "$PWD/linear/kitt-linear" ~/.local/bin/kitt-linear
```

Turn on **Custom script** in Linear (Settings → Code & reviews → Configure coding tools), then `~/.linear/coding-tools.json`:

```json
{
  "openIssue": {
    "path": "/Users/<you>/.local/bin/kitt-linear",
    "args": ["{{issue.identifier}}", "--branch", "{{issue.branchName}}", "--dir", "{{workDir}}"],
    "env": ["LINEAR_PROMPT"]
  }
}
```

The prompt comes through the environment, not the arguments, so no text of it is read as a flag. `--dir` picks the registered repo the working directory lies in. Linear runs the script outside any shell and does not wait: it sets its own PATH, works in the background into `~/Library/Logs/kitt-linear.log`, starts herdr in Terminal when no herdr server runs (`KITT_TERMINAL=iTerm` for iTerm), and reports a failure as a notification.

The same from the shell: `kitt new ENG-123 --branch tjark/eng-123-fix-login --prompt "…"`. A lane is then also named by its ticket: `kitt focus ENG-123`.

## How lanes stay apart

- **Ports.** Each lane holds a slot; an app listens on its base port plus ten per slot (Metro 8081 in the main checkout, 8091 in the first lane). `kitt env` prints a lane's ports.
- **Env files.** The gitignored files named under `link` are symlinks into the main checkout, so every lane sees the same secrets and an edit reaches all of them.
- **The emulator.** One device, one installed dev client. `kitt emu` reverses the lane's ports into it and sends the client to the lane's Metro; no native build. A proof holds a lock, so two agents never load over each other.
- **Shared apps.** An app marked `shared` runs once, from the main checkout, for every lane (a database, a backend whose port is fixed).

## kitt.toml

Everything project-specific lives in the repo, in `kitt.toml`. `kitt init` writes it:

```
kitt detect <path>      # look first: what kitt sees in a repo; registers and writes nothing
kitt init               # the wizard: each detected piece shown to keep, edit or drop, then written and registered
kitt init --agent       # an agent reads the repo first (justfile, Makefile, CI, AGENTS.md) and fills in dev commands, checks, ports
kitt init --home        # write into kitt's own folder, for a repo that is not yours to add a file to
```

Detection alone finds the apps (Expo, Next, Vite, anything with a `dev` script, a Python backend), their checks from `package.json` scripts, the package manager from the nearest lockfile, and the gitignored `.env` files to link. What it cannot see, the agent or you add: which env file means local, how the apps find each other, what CI really runs.

Which apps a lane touched is never stored. kitt reads it from git each time: the files that differ from the base branch, matched to the app whose `dir` they lie in.

```toml
name = "schlemm"
base = "master"
link = ["apps/mobile/.env", "apps/mobile/.env.supabase.local"]

[emulator]
reverse = [54321]            # ports besides the apps' own the device must reach

[proof]
guide = "docs/proof.md"      # or the text itself: test accounts, how to reach a screen

[[app]]
name = "mobile"
kind = "expo"                # expo | web | backend
dir = "apps/mobile"
port = 8081
dev = "bunx expo start --dev-client --clear --port {port}"
env_files = [".env.supabase.local"]
setup = ["bun install --frozen-lockfile"]
checks = ["bun run typecheck", "bun run lint", "bun run test"]

[app.env]
EXPO_PUBLIC_API_URL = "http://localhost:{port:api}"
```

`{port}`, `{port:<app>}`, `{lane}`, `{slot}`, `{root}` and `{main}` are filled per lane. `lazy = true` defers an app's setup and start until it is named; `shared = true` runs it once for all lanes.

`dev` runs in a herdr pane, in your shell; `setup` and `checks` run through `cmd /C` (`sh -c` elsewhere).

Until a repo has its own `kitt.toml` on its base branch, a copy at `<kitt home>/repos/<repo folder>.toml` stands in; the repo's file wins once it exists.

## Proof

```
kitt proof begin             # takes the emulator, loads the lane's app, prints the repo's guide
kitt proof shot "one section, no header"
kitt proof end --pass        # or --fail --note "..."
kitt proof open              # the page: verdict, note, shots in order
```

Proofs are kept under kitt's own folder (`%APPDATA%\kitt\proofs`), outside the repo. A proof goes stale when the lane gets a new commit.

## pr-watch

Shown above the prompt, only when there is something to act on.

A key never runs git itself. It sends Claude a few words, with the how-to attached where only Claude reads it. Claude decides whether and how to act and calls one of the mod's tools; the tool does the fixed steps and answers with what happened. So every action is a real tool call in the transcript that Claude can decline, redo with other arguments, or react to.

| Shown | Key | What happens |
|---|---|---|
| the open PR: status, issue, one chip per check, the failing step and log lines | `1` `2` `3` `x` | retry failed jobs · hand the log to Claude · open in browser · hide |
| uncommitted work | `c` | Claude writes the message the repo asks for and calls `commit`; no push |
| commits not pushed, no PR yet | `p` | Claude runs the repo's checks, calls `push`, opens the PR |
| commits not pushed, PR open | `p` | Claude calls `push` |
| behind the default branch | `b` | Claude calls `rebase_and_push`: the branch is rebased onto the default branch and then pushed to its own remote branch, ready for a PR; on conflicts Claude rebases by hand and asks where a conflict is not clear |
| the PR is merged | `m` | switches to the default branch, brought up to date (done by the mod; Claude is told) |

**Merging is yours.** No key and no tool merges. When Claude runs `gh pr merge` anyway, the call is held and you are asked; anything but "Merge" refuses it.

**Auto-rebase** is an option of the plugin, off by default (`/plugin`, pr-watch, configure). On, a branch with an open PR that falls behind is rebased and pushed without a key press, as long as the tree is clean and the rebase has no conflicts.

The tools, as Claude sees them:

- `mcp__pr-watch__commit` `{ subject, body?, paths? }`: stages and commits. Refuses on the default branch. Never pushes.
- `mcp__pr-watch__push` `{ force_with_lease? }`: pushes. A refused push answers with the commits the remote holds; forcing then overwrites exactly those.
- `mcp__pr-watch__rebase_and_push`: rebases onto the default branch and pushes with lease. A conflicting rebase is undone and answered with the files.

The branch name sits at the end of the hint line. `/pr-watch` opens the full view of the PR. Keys work once the band has focus (click it, or ctrl+x tab).
