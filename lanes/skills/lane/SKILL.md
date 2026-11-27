---
name: lane
description: Work inside a kitt lane — a worktree with its own ports and a shared emulator. Use when `kitt env` works in the current directory, when asked to prove a change in the app, or when a dev server, Metro port or the emulator is involved in a repo that has a kitt.toml.
---

# Working in a kitt lane

A lane is one checkout of the repo for one ticket. Several lanes run side by side, so nothing here is on its default port and the emulator is shared.

## Know where you are

```bash
kitt env        # this lane's name, slot and the port of each app
kitt status     # the same, plus who holds the stack and which database the lane's env files point at
```

`kitt status` works in any lane and in the main checkout. There is no other status command; do not guess one.

Use those ports. Never assume 8081, 8000 or 3000: they belong to the main checkout.

`kitt who` says whether the repo runs one stack at a time (`single`) and which lane holds it. In the single mode there is one stack: run `kitt claim --wait 10m` before you drive the app, `kitt release` when you are done, and never `kitt up` another lane's app.

The env files in a lane are symlinks to the main checkout. Read them, do not edit them: an edit changes every lane.

### Which database a lane talks to

The linked env files decide, and they are the main checkout's: every lane shares them. `kitt status` prints, per env file, the database host and the project ref (never keys or passwords). Read that line before you run anything that writes (a migration, a seed, a test that cleans up) and say in your report which project it was. A repo that keeps a local database in a branch's own migrations needs the app and the API pointed at it again after every `kitt up`, because `kitt up` rewrites the lane's `env_out` file; check `kitt status` after it. Never put a database URL or a key into a command line, a commit or your report: the host and the ref are enough.

## Run the app

```bash
kitt up              # start this lane's dev servers on its ports
kitt up admin        # an app that is not started by default
kitt down            # stop them
```

`kitt up` leaves alone whatever already answers on its port. In the single mode it also takes the stack from the lane that had it, unless that lane is in the middle of a test.

## Check before you report

```bash
kitt check           # the checks of every app this lane touched, then the repo's own (lint over the diff, coverage)
kitt check --all     # every app's checks
```

The checks are the repo's own (kitt.toml) and are meant to equal what CI runs, so a green `kitt check` means CI will not find these problems after the push. Besides each touched app's tests, lint and type-check, the repo's checks cover the whole diff against the base (`{base}` is `origin/<base branch>`: run `git fetch` first if the base moved). `kitt check` exits 1 when any check fails. Fix what fails; do not report done over a red check. A step CI only reports (diff coverage) is report-only here too: read its output, it does not fail the run.

## Hand a slice to an implementer: kitt fork

```bash
kitt fork <slice>            # in the lane: a sibling worktree <lane>-<slice>, branch <lane-branch>-<slice>, from the lane's tip, installed
kitt fork <slice> --apps api # only that app's setup (a Python venv is slow)
```

Use it when an implementer agent (not you) works on one slice and must commit. Do not start agents in a bare `git worktree` or `.claude/worktrees` checkout: they start on the base branch, not on the lane's tip, and a fresh worktree has no installed dependencies and no hook shims (`core.hooksPath` points at a folder the install writes), so git skips the hooks silently or a linked `node_modules` breaks them. A fork is installed like a lane, so a commit in it runs the repo's real hooks. `kitt fork` warns when the hooks folder is still missing.

Commits come back into the lane with `git merge <fork branch>` (or cherry-pick). Then `kitt rm <fork lane>` removes the worktree and the branch, without `--force`; it refuses while the fork holds commits the lane's branch lacks. Remove your forks when the slice is merged.

## Clean up merged lanes

```bash
kitt rm --merged --dry-run   # which lanes would go, and why the others stay
kitt rm --merged             # remove them
```

A lane goes when its PR is merged, its tip is on the remote and its tree is clean. A lane with unpushed commits, uncommitted files or an unmerged PR stays, with the reason printed. Never `kitt rm --force` a lane you did not create.

## Forbidden

- Never `--no-verify`, `HUSKY=0` or any other way around a repo's hooks. If a hook fails, fix what it reports; if it cannot run, say so and stop.
- Never change the simulator's or emulator's text size or Dynamic Type, and do not ask for proofs at large text sizes.
- Never `--force` past another lane's hold on the stack or the emulator.
- Never edit a linked env file (it is the main checkout's) or print its secrets.

## Prove a change that is visible in the app

The emulator is one device for all lanes: an Android emulator through adb, or on a Mac a booted iOS simulator. kitt picks whichever runs (Android first); `KITT_PLATFORM=ios` or `android` chooses.

Who may use it is a lease. `kitt focus` and `kitt up` leave a soft one: the next lane simply takes over. A proof or a claim holds a hard one: other lanes wait until it is released. `kitt proof begin` takes the hard lease (in the single mode the whole stack: it starts this lane's servers and stops the other lane's), loads this lane's app, and `kitt proof end` gives it back.

```bash
kitt proof begin                 # waits for the emulator, loads this lane's bundle, prints the repo's proof guide
# drive the app; begin prints how for the device it picked:
#   Android: adb shell input tap / swipe / text, uiautomator dump for coordinates
#   iOS: axe or idb when installed (tap, type, describe-ui); else deep links: xcrun simctl openurl <udid> <scheme>://<path>
kitt proof shot "recipe with one section"     # one shot per state worth showing; prints the file, read it to verify
kitt proof end --pass            # or: --fail --note "what is wrong"
```

- Begin only when the code is ready to be looked at; end as soon as you have the shots. Other lanes wait while you hold the emulator.
- If `kitt proof begin` or `kitt claim` fails after its wait, report "stack held by <lane>" and stop. Do not use `--force`: taking a stack from another agent's test is the person's decision.
- Look at each shot you take. A proof of a broken screen is a `--fail`, with a note that says what is wrong.
- For a web app, take the screenshots with the browser tool and add them: `kitt proof add shot.png "empty state"`, with `kitt proof begin --no-emulator`.
- Commits after a proof make it stale. Prove again after the last change.

Say in your report that the proof exists and what it shows; the person opens it from the dashboard.
