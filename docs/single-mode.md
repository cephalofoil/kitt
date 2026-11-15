# Single mode and env_out, as implemented

This page describes what the code on this branch does. It was written by reading
`cmd/kitt/stack.go`, `emu.go`, `proof.go`, `dev.go`, `config.go`, `phone.go`,
`proc_*.go` and the tests, not from a spec.

**The spec is not in the repo.** Both features come from issue #5 ("Kitt braucht
ein Lock"). The pull requests that built them (#6 single mode, #7 env_out) say
they cover "spec tests 1–11 and 13" and refer to a "spec PR 3". That spec was
never committed, and it is not reproduced or reconstructed here. So:

- The numbers 1–11 and 13 refer to that spec, not to anything in this repo. The
  table [Tests](#tests) maps the tests that exist to the behaviour they cover,
  without spec numbers.
- Test 12 of the spec is unaccounted for: no PR says what it was or why it was
  left out.
- "Spec PR 3", process supervision without herdr, is not implemented. The single
  mode needs herdr (see [Known limits](#known-limits-as-of-this-branch)).

## Config

```toml
[stack]
mode = "single"     # "parallel" (the default, also any other value): every lane runs its own servers
keep = ["db"]       # apps the single mode never stops, beside the shared ones
lease_ttl = "20m"   # how long a hard lease holds without a word from its holder
```

- `mode`: only the exact string `"single"` turns the single mode on. Without it,
  every function below does nothing and no lease file is written. On Windows
  the single mode is refused (see [Windows](#windows)).
- `keep`: app names. The **stack apps** are all apps that are neither
  `shared = true` nor named in `keep`. Only stack apps are stopped on a takeover.
- `lease_ttl`: a Go duration (`time.ParseDuration`). Missing, unparsable, zero or
  negative means 20 minutes.

## The lease

One lease per repo, a JSON file at `<kitt home>/stack-<repo>.lock`. The kitt home
is `$KITT_HOME`, else `os.UserConfigDir()/kitt` (`~/.config/kitt` on Linux,
`~/Library/Application Support/kitt` on macOS, `%APPDATA%\kitt` on Windows).

```json
{
  "repo": "example-app",
  "lane": "<normalised lane path>",
  "name": "settings-proto",
  "kind": "hard",
  "reason": "claim: maestro login flow",
  "holder": 41237,
  "since": "2026-10-06T12:00:00Z",
  "renewed": "2026-10-06T12:04:10Z",
  "path": "/…/example-app-lanes/settings-proto",
  "main": "/…/example-app",
  "slot": 3
}
```

`path`, `main` and `slot` are kept so the next lane can stop this lane's servers.
The file is replaced in one step (written to `<file>.<pid>.tmp`, then renamed).
Every read-modify-write of it runs under a mutex: a directory
`<kitt home>/stack-<repo>-mutex.lock` made with `mkdir`. A caller waits up to 3
seconds for it; a mutex directory older than 10 seconds is taken as left behind
and removed.

### States

| State | Lease file | Keeps other lanes out |
|---|---|---|
| free | none | no |
| soft | `kind: "soft"` | no: the next lane takes over |
| hard | `kind: "hard"` | yes, while it is not stale |
| hard, stale | `kind: "hard"` | no: treated like soft; `kitt who` shows `hard (stale)` |

A hard lease **blocks** when all three hold: its holder process is alive, it was
renewed less than `lease_ttl` ago, and the caller did not pass `--force`.

### Which command does what

| Command | Effect on the lease | Waits | `--force` |
|---|---|---|---|
| `kitt up` (not `--print`) | soft, reason `up` | no, fails at once | no |
| `kitt new --up` | soft, reason `up` | no, fails at once | yes |
| `kitt focus` (servers on, the default, or `--restart`) | soft, reason `focus` | no, fails at once | yes |
| `kitt focus --no-up` | none | – | – |
| `kitt focus --restart --no-up` | refused as a usage error: the two exclude each other | – | – |
| `kitt emu` | soft, reason `emu` | no, fails at once | yes |
| `kitt phone` (not `--print`) | soft, reason `phone` | no, fails at once | no |
| `kitt claim [--reason …]` | hard, reason `claim` or `claim: <text>`, then starts the lane's servers | `--wait`, default 0 | yes |
| `kitt proof begin` | hard, reason `proof` | `--wait`, default 10m | no |
| `kitt proof shot`, `kitt proof add` | renews (lease and emulator lock) | – | – |
| `kitt proof end` | hard → soft; servers keep running | – | – |
| `kitt release` | hard → soft; servers keep running | – | – |
| `kitt release --down` | lease file removed, then the lane's stack apps stopped | – | – |
| `kitt down` | stops the lane's servers, then removes the lease if this lane holds it (hard or soft) | – | – |
| `kitt rm` | stops the lane's servers, removes the lease if this lane holds it, gives the emulator back | – | – |
| `kitt who [--json]` | reads only | – | – |

The dashboard's `enter`, `n`, `b`, `u`, `d`, `e` and `x` keys run these commands
as their own processes, so they follow the same rules; `h` runs `kitt phone`'s
code in the dashboard's own process, with the same effect on the lease.

When the single mode is on and another lane holds a hard lease that blocks
(holder alive, renewed within `lease_ttl`), the open-lane dialog (`enter` on a
lane, `n`, `enter` in the backlog) adds one choice at the end, never the one it
starts on: `take over the stack (ends <lane>'s <reason>)`. It runs
`kitt focus <lane> --force` for a lane that exists, or
`kitt new … --blank --up --focus --force` for one to be made. No other choice
passes `--force`.

When the lane taking the stack already holds it:

- the lease is renewed (`renewed` set to now) and `since` is kept;
- a **soft** take keeps the kind, reason and holder as they were, so `kitt up`
  during a proof leaves the proof's hard lease hard;
- a **hard** take makes it hard with the new reason and holder.

So any `up`, `focus`, `emu`, `claim`, `proof shot` or `proof add` of the holder's
lane renews a hard lease.

A caller that is blocked and has time left prints `waiting: <who holds it>` once
and polls every 3 seconds. When the time is up it fails with:

```
the stack of <repo> is held by <name> (<reason>, since <ago>): wait for it (kitt claim --wait 10m), or ask its holder to `kitt release`
```

`kitt claim`, `kitt up`, `kitt focus` and `kitt phone` fail in the single mode
when herdr is not there:

```
the single mode needs herdr to start and stop a lane's servers (or kitt up --print, and run them yourself)
```

`kitt up --print` and `kitt phone --print` take no lease.

### Staleness: who the holder is

`holder` is the process a hard lease lives as long as. kitt's own parent is often
a shell made for one command (an agent's tool call) that ends when kitt does, so
on macOS and Linux kitt takes its **grandparent**: it runs
`ps -o ppid= -p <parent pid>` and uses the answer when it is a number above 1,
else the parent. On Windows it uses the parent; that is one reason the single
mode is refused there (see [Windows](#windows)). The emulator lock outside the
single mode still uses it.

Liveness is `kill(pid, 0)` on Unix (EPERM counts as alive) and
`OpenProcess` + `GetExitCodeProcess == STILL_ACTIVE` on Windows.

A hard lease goes stale when its holder is gone or it was not renewed for
`lease_ttl`. A stale lease is not cleaned up by itself; the next lane to take the
stack displaces it like a soft one.

### The emulator

The emulator lock is a separate file, `<kitt home>/emulator.lock`
(`{lane, name, at, holder}`), shared by every repo, guarded by the mutex
`emulator-mutex`. It is fresh while `at` is less than 20 minutes old (fixed, not
`lease_ttl`) and its holder (same `holderPid` rule; 0 for an old lock) is alive.

- A hard lease on a lane that has an Expo app takes the emulator too, in the same
  step. If another lane holds a fresh emulator lock (and it is not the lane being
  displaced, and there is no `--force`), the hard take waits or fails with
  `the emulator is held by <name> (since <ago>)`.
- A soft take of the stack from another lane releases the displaced lane's
  emulator lock.
- `release`, `proof end`, `down` (when it drops the lease) give the emulator back.
- `kitt emu` and `kitt focus` refuse to load a lane while another lane's fresh
  emulator lock exists.
- Outside the single mode, `kitt proof begin` takes the emulator lock alone
  (waits up to `--wait`).

## Takeover

When a lane takes the stack from another (a soft lease, a stale hard lease, or a
hard one with `--force`), kitt first writes the new lease under the mutex, then
stops the old lane's stack. For the old lane's slot:

1. **The dev tab.** The old lane's herdr tabs labelled `dev` or `dev:…` are
   closed, which ends every server running in them (the phone Metro of
   `kitt phone` included), unless some app that is not a stack app (shared or
   kept) has a `dev` command and answers on its port; then no tab is closed,
   and the phone Metro's own pane (recorded in `state.json` as `phone_pane`) is
   closed instead, when the expo app is a stack app.
2. **Stop lines.** Each stack app's `stop` line runs (`sh -c`, or `cmd /C` on
   Windows) in the app's directory. Its result is reported as `stopped` or
   `stop failed: <first line>`.
3. **Ports.** For each stack app in turn, kitt waits up to 10 seconds (20 × 0.5 s)
   for its port in the old lane's slot to stop answering on `127.0.0.1`. If it
   still answers, kitt asks `lsof -t -iTCP:<port> -sTCP:LISTEN` for the pids and
   kills each one (`os.Process.Kill`, SIGKILL on Unix): `still on <port>: killed <pids>`.
   When no pid is found it says `still on <port>: stop it by hand`. When the
   lane has had a phone Metro (`phone` or `phone_pane` set in its state) and
   the expo app is a stack app, its port (`phonePort`: the first port after the
   lane's Metro that no app has) is freed the same way, as `<app> phone`.
   Afterwards the lane's phone record is cleared.

These lines are printed under `stopped <old lane>`. Then the new lane's servers
start (`up`, `focus`, `claim`), or the emulator is pointed at it (`emu`).

## env_out

### Why

`[app.env]` reaches a dev server as its process environment. The premise of
#7 was that Expo merges `.env`, `.env.local`, `.env.development.local` and the
like into the client bundle over `process.env`, so a lane whose `.env.local` is
linked from the main checkout would get the main checkout's values (its API
URL, its ports) instead of its own. `env_out` puts the lane's values into a
file the bundler reads. Per its source, Expo CLI's own loader does not do
that (see the note under [What is written](#what-is-written)); `env_out`
matters where something else reads the files with them winning.

### What is written

`env_out = "<file>"` on an `[[app]]` is a path relative to the app's directory.
Each time `kitt up` (also through `focus`, `claim`, `proof begin`) **starts** that
app in herdr, it first writes:

- a header line `# Written by kitt up for this lane; kitt rewrites it on every start.`
- the app's `[app.env]` values with the lane's placeholders filled in, and its
  `port_env` set to the lane's port;
- one `KEY=value` line each, sorted by key. Values are written for the dotenv
  parsers bundlers use (the `dotenv` package; Node's `util.parseEnv` in
  `@expo/env` 2.x), neither of which unescapes `\\` or `\"` inside quotes:
  every `$` is written `\$` (dotenv-expand expands `$VAR` quoted or not); a
  value with a line break or CR and no `"` is double-quoted with them as `\n`
  and `\r` (Node ignores `\r`); any other value with a space, tab, `#`, quote,
  backtick or line break is double-quoted when it has no `"`, else
  single-quoted when it has no `'`, else backtick-quoted, as it is (line breaks
  raw: both parsers read multi-line quoted values). A `\` alone needs no quotes.

Never written: the values of the app's `env_files`. They still reach the process
environment, but are not copied into `env_out`, since they are secrets as often
as not.

The file is written with mode 0600, to `<file>.<pid>.tmp` first and then renamed
over the target. If the target is a **symlink**, kitt refuses
(`env_out <path> is a symlink; choose a lane-local file`) and that app is not
started: a linked file belongs to the main checkout, and a lane's ports in it
would reach every lane. After writing, kitt runs `git check-ignore -q` on it and
prints `warning: env_out <file> is not gitignored` when git would commit it.

`kitt up --print`, and `up` without herdr, write nothing; they print
`(and writes <path>)` after the command instead. An app that is already running
and unchanged is not touched, so its `env_out` is not rewritten.

Which file to pick: the one the bundler gives the highest precedence among the
files present. For Expo in development, `.env.development.local` wins over a
linked `.env.local`.

For an app of kind `expo`, each time `kitt up` writes `env_out` it also checks
the repo's `link` list: a linked file in the app's directory that Expo reads
before `env_out` (order `.env.development.local`, `.env.local`,
`.env.development`, `.env`; a name not in that list ranks below all four) gives

```
warning: Expo reads linked .env.local before env_out .env.development, so the main checkout's values win: name env_out .env.development.local
```

`kitt doctor`, run inside the repo, prints the same. It is a warning, not an
error. Other kinds are not checked: kitt's `web` may be Vite (which puts
`.env.development` above `.env.local`) or Next (Expo's order), and kitt cannot
tell which.

**What `@expo/env` does with the process environment.** Its source (read for
0.0.5, 0.3.0 and 2.6.0) does not overwrite a variable the process already has:
a key that kitt passes as the pane's environment wins over every `.env` file.
By that reading, an `[app.env]` key reaches Expo CLI's Metro with the lane's
value even without `env_out`; `env_out` matters where something reads the
files itself (an `app.config.js` that calls `dotenv` with `override`, a babel
plugin that reads `.env`), or where Metro is started without kitt's
environment. This was not tried against a running Expo here.

### Restart on change

When kitt starts an app it records, in `state.json` under the lane's `dev`, the
herdr pane and a hash of the command (expanded), the app directory, the
`env_out` name and the full environment (env files' values, then `[app.env]`,
then `port_env`). On the next `kitt up` (also via `focus`, `claim`,
`proof begin`), an app whose port answers is:

- left alone (`already up on <port>`) when kitt has no record of starting it,
  when the hash matches, with `--print`, or without herdr;
- otherwise restarted: its recorded pane is closed, its stop line runs, its port
  is freed (the same 10 s wait, then kill, as a takeover), and it starts again
  (`its config changed: restarting`). `up` returns the names of the apps it
  restarted (`upApps`), and `focus` reports them as
  `restarted dev servers: their config changed`.

Since the hash includes the env files' values, editing a linked `.env` also
restarts the app on the next `up`. An app started by a kitt from before this
change has no record, so it is left running with its old environment until a
`kitt down` and `kitt up`.

### The phone Metro and env_out

`kitt phone` starts a second Metro in the same app directory as the lane's,
with the app's environment (`appEnv`: its env files, `[app.env]` and
`port_env`, with the phone Metro's port), every `localhost`/`127.0.0.1`
address in it replaced by this machine's LAN address, and
`REACT_NATIVE_PACKAGER_HOSTNAME` set to that address. This is the same for
every app, `env_out` or not. kitt reads none of the `.env` files in the app's
directory for it and does not turn off Expo's loading of them.

The `env_out` file in that directory holds the lane Metro's `localhost` values,
and both Metros may run at once, so the file is not rewritten. Expo CLI's
loader still reads it, but by `@expo/env`'s source (2.6.0, `loadEnvFiles`;
read, not run) a key the process already has is not overwritten, so the
LAN values kitt passes win for every key kitt sets.

Limits:

- A `localhost` URL that lives only in a `.env` file kitt does not read (not
  in `[app.env]` or the app's `env_files`) reaches the phone Metro as it is,
  through Expo's loader, and is not rewritten.
- Where something other than Expo CLI's loader reads the `env_out` file with
  the file winning (an `app.config.js` that loads dotenv with `override`,
  `react-native-dotenv`), the phone Metro gets the lane's `localhost` values
  from it.

## Windows

`[stack] mode = "single"` is refused on Windows: `acquireStack` and
`needsHerdr` (so `up`, `new --up`, `focus`, `emu`, `phone`, `claim`,
`proof begin`) fail with

```
the single mode ([stack] mode = "single") is not supported on Windows yet: kitt can neither tell which session holds the stack nor free a lane's ports there. Remove [stack] from kitt.toml to run lanes in parallel as before
```

and `kitt doctor`, run inside such a repo, prints it. The reasons: `holderPid`
has no grandparent lookup there (an agent's one-command shell ends at once, so
a hard lease is stale at once), and `listeners()` finds no pid on a port, so a
takeover frees nothing. `kitt up --print`, `kitt who`, `kitt ls`, `release`,
`proof shot`/`end` and everything in the parallel mode work as before. The
check reads `onWindows`, a variable, so the tests can set it.

## Tests

`go test ./cmd/kitt` runs these. The stack tests replace process stopping,
liveness and the clock with fakes; they do not start servers, call herdr or kill
anything.

| Test | Covers |
|---|---|
| `TestStackParallelModeDoesNothing` | without `mode = "single"` no lease and no lease file is written |
| `TestStackFirstLaneGetsSoftLease` | the first lane gets a soft lease naming it; nothing is displaced |
| `TestStackSoftLeaseIsTakenOver` | a soft lease is taken by the next lane, and the old lane is stopped once |
| `TestStackHardLeaseKeepsOthersOut` | a hard lease refuses another lane's soft take, the error names holder and reason, nothing is stopped |
| `TestStackHardWaitsForRelease` | a blocked caller waits until its deadline, and gets the stack once the holder releases |
| `TestStackForceDisplacesHard` | `--force` displaces a live hard lease and stops its lane |
| `TestStackDeadHolderIsStale` | a hard lease whose holder process is gone is taken over |
| `TestStackQuietHolderIsStale` | a hard lease not renewed for 20 min (default) or a repo's own `lease_ttl` is taken over; `renewStack` resets the clock |
| `TestStackHardRace` | eight lanes asking for a hard lease at once: exactly one gets it |
| `TestEmulatorRace` | eight lanes taking the emulator at once: exactly one gets it |
| `TestStackRelease` | `release` makes a hard lease soft and stops nothing; `release --down` removes the lease and stops the lane |
| `TestStackHolderStaysHard` | the holder's own soft take (`up` during a proof) keeps the lease hard with its reason |
| `TestStackAppsLeaveSharedAndKept` | shared apps and those in `keep` are not stack apps; a lazy app is |
| `TestWhoJSON` | `kitt who --json` shows the repo, mode, lease (name, kind, reason, since), not stale, emulator `null` |
| `TestEnvOutWritesLaneValues` | the process env is env files, then `[app.env]` over them; `env_out` holds only `[app.env]` and `port_env`, sorted, quoted, ports of the lane's slot, mode 0600, never the env file's secret |
| `TestEnvOutRefusesSymlink` | `env_out` that is a symlink is refused and its target is left unchanged |
| `TestEnvOutIgnoredCheck` | the `git check-ignore` check tells an ignored file from one git would commit |
| `TestDevHash` | the hash ignores map order and changes with the env or the command |
| `TestEnvValue` | values with line breaks, quotes, backslashes and `$` are written so dotenv reads them back |
| `TestEnvOutOutranked` | the Expo order of env files, a linked file above `env_out` named, `web` apps not checked |
| `TestStackSingleRefusedOnWindows` | on Windows the single mode is refused by `acquireStack` and `needsHerdr`, no lease written; parallel mode and `kitt who` work |
| `TestFocusRestartNoUpRefused` | `kitt focus --restart --no-up` is a usage error and takes nothing |
| `TestTakeoverOpening` | another lane's hard lease adds `--force` as the last choice, never the starting one, naming holder and reason, for a lane and a new lane |
| `TestBlockingLease` | only a live hard lease of another lane in the single mode offers the takeover; enter on a lane shows it |
| `TestStackPortsIncludePhone` | a takeover frees the phone Metro's port once the lane has had one, not when the expo app is kept |
| `TestPhoneTakesStack` | `kitt phone` takes a soft lease and stops the lane before, fails against a hard one, takes none with `--print`, needs herdr |
| `TestPhoneMetroEnv` | with or without `env_out`: the app's env files, `[app.env]` and the phone port with LAN addresses; no `EXPO_NO_DOTENV`, nothing read from the app dir's `.env` files |
| `TestRmDropsLease` | `kitt rm` removes the lane's lease and emulator lock |
| `TestMainCheckoutEntryIsNoLane` | a state entry for the main checkout keeps it the main checkout at slot 0, and the next lane gets slot 1 |

Not covered by a test: the takeover's tab and phone pane closing, stop lines and
port killing (`stopStack`, `freePort`), the grandparent lookup, and the
restart-on-change path inside `up` (which needs herdr and a server on the port).

## Known limits (as of this branch)

- **The single mode needs herdr.** kitt starts servers only in herdr panes and
  can only be sure to stop what it started. `up`, `focus` and `claim` fail without
  it. There is no supervision without herdr ("spec PR 3").
- **A takeover kills whatever listens on the old lane's ports.** After the
  10-second wait, every process `lsof` reports listening on a stack app's port in
  the old lane's slot is killed, whoever started it: a server started by hand
  in a terminal included. The same applies to a restart on config change.
- **The grandparent heuristic** is checked for Claude Code (its tool calls run
  in a shell per command) and for a person's terminal shell. It is not verified
  for opencode or other agent hosts; if their command runner nests differently,
  the holder may be a process that ends too early (the lease goes stale at once)
  or one that outlives the session (the lease holds until `lease_ttl`).
- **Windows** refuses the single mode (see [Windows](#windows)).
- **Without `lsof`** on macOS or Linux, a port that still answers after 10
  seconds is not freed: kitt prints `still on <port>: stop it by hand`.
- **No `--wait` for `up`, `focus` or `emu`.** They fail at once against a live
  hard lease; only `claim` and `proof begin` wait.
