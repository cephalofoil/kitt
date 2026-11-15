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

`kitt doctor` says what is missing (git, gh, herdr, adb or the iOS simulator, symlinks) and which device kitt would use.

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
kitt new settings-proto --prompt "/wayfinder settings across several screens"
kitt new tryout --blank --up  # no ticket: a lane, a blank agent to talk to, its dev servers running
```

The agent opens as soon as the worktree is there; the install runs after it, and a prompt waits for the install.

An agent put on an issue is told where it ends: the change built and checked, seen working in the app and recorded as a proof, then a pull request that closes the issue and says what the proof shows. It does not merge. `[agent] prompt` in `kitt.toml` replaces that text; `{issue}`, `{title}` and `{url}` are filled in.

In the dashboard, one row per lane: the apps it has changed files of, the agent's state, uncommitted and behind counts, the PR and its checks, the last proof, which dev servers answer, and which lane the emulator shows.

| Key | |
|---|---|
| `enter` | go into the lane, after a dialog that offers what fits its state: go to it as it is (first, when it is open), a blank Claude or one more beside the one at work, its dev servers started or restarted with its app in the emulator, Claude put on the issue or told what you type |
| `g` | start the lane's agent (on its issue, if it has one) |
| `e` | only point the emulator at the lane |
| `h` | load the lane on a phone on the Wi-Fi: a second Metro told this machine's address, and the code to scan |
| `o` | open the lane's web app in the browser |
| `i` | install what the lane runs, when a lane shows `no install` (installs run one lane at a time) |
| `u` / `d` | start / stop the lane's dev servers |
| `c` | run the repo's checks for the apps the lane touched |
| `p` | open the lane's proof |
| `n` | new lane: an issue number or a name, then a dialog for how it opens: a blank Claude, a blank Claude with the dev servers, or Claude prompted (with the issue, or what you type) with the dev servers |
| `b` | the backlog: the repo's open issues, the one under the cursor to read beside them (`space` reads on, `/` filters by words, a label or a number, `tab` goes to the next repo). `enter` on one makes its lane, after the same dialog: a blank Claude, or Claude put on the issue through to a pull request. An issue that has a lane already is gone into |
| `a` | adopt a worktree kitt did not create (Claude's, herdr's, a hand-made one) |
| `x` | remove a lane that holds nothing unsaved |
| `t` | show the worktrees that are not lanes |

Under the lanes, the open pull requests that have no lane yet (a bot's are left out). `enter` on one fetches its branch and checks it out as a lane about the apps it touches; `o` shows it on GitHub. The same from the shell: `kitt pr 380`.

Every key is also a command: `kitt focus`, `kitt agent`, `kitt backlog`, `kitt emu`, `kitt phone`, `kitt up`, `kitt check`, `kitt proof open`, `kitt adopt`, `kitt rm`. See `kitt help`.

### The header

The dashboard opens with the kitt wordmark and, beside it, three facts: how many lanes there are, whether an agent is waiting for you, and which lane the emulator shows. Below 40 columns the wordmark gives way to the plain name.

`KITT_LOGO` picks another mark; `kitt logo` prints them all:

| Value | |
|---|---|
| not set | the wordmark, four lines |
| `lockup` | the tube beside a narrower wordmark, four lines |
| `small` | tube and wordmark in three lines |
| `tube`, `bead` | the tube alone, without or with a bead of putty |
| `medium`, `large`, `blocks` | other sizes; `blocks` draws the tube with half blocks only |
| `text` | no logo |

The tube is drawn with Unicode sextants (U+1FB00 and up). A font without them shows boxes there; the wordmark uses only block elements and draws everywhere.

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

`kitt-linear` runs on macOS only: it is a zsh script that uses `osascript` and `~/Library/Logs`. There is no Windows counterpart yet.

Linear's prompt is kept in plain text in kitt's `state.json`, like any `--prompt` given to `kitt new`, so `g` can start the agent on it again.

The same from the shell: `kitt new ENG-123 --branch tjark/eng-123-fix-login --prompt "…"`. A lane is then also named by its ticket: `kitt focus ENG-123`. Two branches that end alike (`tjark/eng-1-x`, `tom/eng-1-x`) would give one lane name; the second is refused with the first lane's branch named, and `kitt new ENG-1 --branch tom/eng-1-x --name eng-1-x-tom` gives it a name of its own.

A Linear lane shows its ticket in the dashboard where an issue lane shows its number, and `kitt ls --json` carries it as `Ticket`.

## How lanes stay apart

- **Ports.** Each lane holds a slot; an app listens on its base port plus ten per slot (Metro 8081 in the main checkout, 8091 in the first lane). `kitt env` prints a lane's ports.
- **Env files.** The gitignored files named under `link` are symlinks into the main checkout, so every lane sees the same secrets and an edit reaches all of them.
- **The emulator.** One device, one installed dev client. `kitt emu` makes the lane's ports reach it and sends the client to the lane's Metro; no native build. A proof holds a lock, so two agents never load over each other; `kitt who` says who has it. A running proof stays on the device it began on: a device started meanwhile does not take its shots, and if that device goes away `kitt proof shot` says so instead of switching.
  - Android (any OS): an emulator or phone through `adb`; the ports are `adb reverse`d.
  - iOS (Mac only): a booted simulator through `xcrun simctl`; it shares the Mac's network, so nothing is reversed. Screenshots come from `simctl io screenshot`. `kitt doctor` says whether `axe` or `idb` is installed; without either an agent can open screens only by deep link. Run inside a repo, it picks the device that repo's `[emulator]` asks for.
  - kitt uses whichever is running, an attached Android device first. `platform = "ios"` (or `"android"`) under `[emulator]`, or `KITT_PLATFORM`, picks one; `serial` names an adb device, `simulator` a simulator by name or UDID.
- **A phone.** A device on the Wi-Fi cannot reach the `localhost` a lane's app is told. `kitt phone` starts a second Metro next to the lane's (8112 beside 8111), with every local address in the app's env replaced by this machine's address on the network, and prints a code the phone's camera opens the dev client with. The lane's own Metro and the emulator are untouched; `kitt down` stops both. `--host` names the address where the machine has several. The replaced values are those kitt passes as the Metro's environment (`env_files`, `[app.env]`, `port_env`); Expo CLI's loader does not overwrite a variable the process already has (read from `@expo/env`'s source, not run), so they win over the lane's `env_out` file in that directory. A `localhost` URL that lives only in a `.env` file kitt does not read is not rewritten for the phone.
- **Shared apps.** An app marked `shared` runs once, from the main checkout, for every lane (a database, a backend whose port is fixed).
- **One stack at a time.** Where a machine cannot run every lane's servers at once, `[stack] mode = "single"` keeps one lane's stack up. See [Single stack](#single-stack).

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
name = "example-app"
base = "master"
link = ["apps/mobile/.env", "apps/mobile/.env.supabase.local"]

[emulator]
reverse = [54321]            # ports besides the apps' own the device must reach
# platform = "ios"           # android | ios | auto (default); KITT_PLATFORM overrides
# simulator = "iPhone 17"    # an iOS simulator by name or UDID; serial = an adb device

[proof]
guide = "docs/proof.md"      # or the text itself: test accounts, how to reach a screen

# [stack]
# mode = "single"            # one lane's servers at a time (default "parallel")
# keep = ["db"]              # apps the single mode never stops, beside the shared ones
# lease_ttl = "20m"          # how long a quiet holder keeps the stack

[[app]]
name = "mobile"
kind = "expo"                # expo | web | backend
dir = "apps/mobile"
port = 8081
dev = "bunx expo start --dev-client --clear --port {port}"
env_files = [".env.supabase.local"]
setup = ["bun install --frozen-lockfile"]
checks = ["bun run typecheck", "bun run lint", "bun run test"]

env_out = ".env.development.local"   # gitignored: kitt writes [app.env] into it on every start

[app.env]
EXPO_PUBLIC_API_URL = "http://localhost:{port:api}"
```

`[app.env]` reaches the dev server as its environment. Some setups read env files into the client bundle with those files winning over that environment, so a linked `.env.local` from the main checkout can send every lane's app to the main checkout's api. `env_out` names a file in the app's directory that `kitt up` writes the lane's `[app.env]` and `port_env` values into, sorted, mode 0600, before it starts the app; env files' values are never copied into it. Pick one the bundler reads last and the repo ignores; kitt warns when git would commit it and refuses a symlink (a linked file is the main checkout's). Expo CLI's own loader is not one of them: it leaves a variable the process already has alone, as its source reads (not run here). `env_out` matters where something else reads the files (an `app.config.js` that loads dotenv with `override`, `react-native-dotenv`) or Metro is started without kitt's environment; more in [docs/single-mode.md](docs/single-mode.md#what-is-written).

The file's name decides whether it wins. Expo reads `.env.development.local`, `.env.local`, `.env.development` and `.env`, the first winning; when `.env.local` is linked from the main checkout, use `.env.development.local`. For an `expo` app, `kitt up` and `kitt doctor` warn when a file under `link` in the app's directory comes before `env_out` in that order (other kinds are not checked: Vite and Next order their files differently).

Values are written so dotenv reads them back as they are: a line break as `\n` in double quotes, a value with a double quote in single quotes, `$` as `\$`.

kitt remembers what it started each app with. When the command, the environment or `env_out` changes, the next `kitt up` or `kitt focus` restarts that app instead of leaving the old one running. This holds only for apps a kitt with `env_out` started: a Metro started before is left running as it is, so after upgrading run `kitt down` and `kitt up` once.

`{port}`, `{port:<app>}`, `{lane}`, `{slot}`, `{root}` and `{main}` are filled per lane. `lazy = true` defers an app's setup and start until it is named; `shared = true` runs it once for all lanes.

`dev` runs in a herdr pane, in your shell; `setup` and `checks` run through `cmd /C` (`sh -c` elsewhere).

Until a repo has its own `kitt.toml` on its base branch, a copy at `<kitt home>/repos/<repo folder>.toml` stands in; the repo's file wins once it exists.

## Proof

```
kitt proof begin             # takes the emulator (and the stack, in single mode), loads the lane's app, prints the repo's guide
kitt proof shot "one section, no header"
kitt proof end --pass        # or --fail --note "..."
kitt proof open              # the page: verdict, note, shots in order
```

Proofs are kept under kitt's own folder (`%APPDATA%\kitt\proofs`), outside the repo. A proof goes stale when the lane gets a new commit.

## Single stack

With `[stack] mode = "single"` a repo runs one lane's servers at a time, and the emulator shows that lane. A lease, kept in kitt's folder, says which lane holds the stack, since when and why.

- `kitt focus`, `kitt up`, `kitt emu` and `kitt phone` take a **soft** lease: the lane that had the stack is stopped (its dev tab closed, its stop lines run, its phone Metro stopped, anything still on its ports killed) and the new lane's servers start.
- A takeover waits ten seconds for each of the old lane's ports to fall quiet, then kills whatever still listens there, a server you started by hand included.
- `kitt proof begin` and `kitt claim` take a **hard** lease, and the emulator with it. Another lane's `focus`, `up`, `emu` or `phone` fails naming the holder; `claim` and `proof begin` wait for it (`--wait 10m`) first. `--force` on `focus`, `emu`, `claim` or `new --up` takes it anyway, a person's call. In the dashboard the dialog of `enter`, `n` and `b` then offers it as a choice of its own, "take over the stack", naming whose test that ends; it is never the one the dialog starts on. `kitt proof end` and `kitt release` make it soft again; `kitt release --down` also stops the servers.
- A hard lease lapses when the session that took it ends, or when it is not renewed for `lease_ttl` (each `kitt proof shot`, `claim` or `up` of the holder renews it). `kitt down` and `kitt rm` give up the lane's lease, and `kitt rm` the emulator too.

```
kitt claim --wait 10m --reason "maestro login flow"   # an agent's test that is not a proof
kitt who                                              # who holds each repo's stack and the emulator
kitt release                                          # done: the stack stays up, another lane may take it
```

`kitt ls` shows the lease above the lanes, the dashboard marks the holder with ⚿. The single mode needs herdr: kitt starts servers only in herdr panes, and stops only what it can find.

The single mode is not supported on Windows yet. There kitt can neither tell which session holds a lease nor find what listens on a port to free it, so a lease would not keep lanes apart; every command that would take the stack refuses, and `kitt doctor` says so. Without `[stack]` lanes run in parallel as before; `kitt up --print`, `kitt who` and `kitt ls` work either way.

[docs/single-mode.md](docs/single-mode.md) describes the lease, the takeover and `env_out` as the code has them, and their limits.

## pr-watch

Shown above the prompt, only when there is something to act on.

A key runs its fixed steps itself, without a turn, and leaves Claude a note of what it did. While it runs, the band says what it is doing; during `kitt check`, the line the check last wrote. The one thing it needs a model for, a commit message or a PR's title and body, it asks of a fork of the session, which answers with text alone.

When the usual case does not hold (a refused push, a failing check, a commit that is rejected, a secret or unrelated changes the fork will not commit as one), the band says in one line what failed, with two keys: `r` resolves it with Claude, `n` cancels. Cancel sends nothing. Resolving sends what the key stands for ("Push this branch and open a PR."), with the reason attached where only Claude reads it; Claude says in a line what went wrong and does the rest, through the mod's tools. The pane shows the whole reason. While a turn is running a key does nothing but say so.

Two keys go to Claude from the start, because that is what they are for: handing over a failing log, and a rebase the check already found conflicts in, since which side wins differs every time. For that rebase a line in the transcript names the files first.

| Shown | Key | What happens |
|---|---|---|
| the open PR: status, issue, one chip per check, the failing step and log lines | `1` `2` `3` `x` | retry failed jobs · hand the log to Claude · open in browser · hide |
| uncommitted work | `c` | commits everything with the message the repo asks for, written by the fork; no push |
| commits not pushed, no PR yet | `p` | runs `kitt check`, rebases if behind and clean, pushes, opens the PR the fork wrote; in a repo without a kitt.toml no checks are run, and the PR says so |
| commits not pushed, PR open | `p` | pushes (done by the mod; Claude is told); a refused push goes to Claude, who reads what the remote holds |
| behind the default branch | `b` | the branch is rebased onto the default branch and then pushed to its own remote branch, ready for a PR: done by the mod when the tree is clean and the check found no conflicts, otherwise Claude calls `rebase_and_push`, rebases by hand on conflicts and asks where a conflict is not clear |
| the PR is merged | `m` | switches to the default branch, brought up to date (done by the mod; Claude is told) |

**Merging is yours.** No key and no tool merges. When Claude runs `gh pr merge` anyway, the call is held and you are asked; anything but "Merge" refuses it.

**Auto-rebase** is an option of the plugin, off by default (`/plugin`, pr-watch, configure). On, a branch with an open PR that falls behind is rebased and pushed without a key press, as long as the tree is clean and the rebase has no conflicts.

The tools, as Claude sees them:

- `mcp__pr-watch__commit` `{ subject, body?, paths? }`: stages and commits. Refuses on the default branch. Never pushes.
- `mcp__pr-watch__push` `{ force_with_lease? }`: pushes. A refused push answers with the commits the remote holds; forcing then overwrites exactly those.
- `mcp__pr-watch__rebase_and_push`: rebases onto the default branch and pushes with lease. A conflicting rebase is undone and answered with the files.

The branch name sits at the end of the hint line. `/pr-watch` opens the full view of the PR. Keys work once the band has focus (click it, or ctrl+x tab).

### In opencode

`opencode/pr-watch` brings the band, its keys and the tools to opencode. It is experimental, a spike, and not published as a package. `.opencode/` in this repo's root loads it, so anyone who starts opencode inside the kitt repo gets it. Its merge guard is weaker than the mod's: merging is held by opencode's own permission prompt, and that prompt offers "always".

## Development

CI runs gofmt, `go vet` (also for Windows and macOS) and `go test -race` on the CLI, and the typecheck and tests of `opencode/pr-watch`. The Claude Code mod's tests run only through `claude plugin test`, not in CI.

## License

[MIT](LICENSE)
