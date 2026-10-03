---
name: lane
description: Work inside a kitt lane — a worktree with its own ports and a shared emulator. Use when `kitt env` works in the current directory, when asked to prove a change in the app, or when a dev server, Metro port or the emulator is involved in a repo that has a kitt.toml.
---

# Working in a kitt lane

A lane is one checkout of the repo for one ticket. Several lanes run side by side, so nothing here is on its default port and the emulator is shared.

## Know where you are

```bash
kitt env        # this lane's name, slot and the port of each app
```

Use those ports. Never assume 8081, 8000 or 3000: they belong to the main checkout.

The env files in a lane are symlinks to the main checkout. Read them, do not edit them: an edit changes every lane.

## Run the app

```bash
kitt up              # start this lane's dev servers on its ports
kitt up admin        # an app that is not started by default
kitt down            # stop them
```

`kitt up` leaves alone whatever already answers on its port.

## Check before you report

```bash
kitt check           # the repo's checks for the apps this lane touched
```

The checks are the repo's own (kitt.toml). Fix what fails; do not report done over a red check.

## Prove a change that is visible in the app

The emulator is one device for all lanes. A proof takes it, loads this lane's app, and gives it back.

```bash
kitt proof begin                 # waits for the emulator, loads this lane's bundle, prints the repo's proof guide
# drive the app with adb: input tap / swipe / text, uiautomator dump for coordinates
kitt proof shot "recipe with one section"     # one shot per state worth showing; prints the file, read it to verify
kitt proof end --pass            # or: --fail --note "what is wrong"
```

- Begin only when the code is ready to be looked at; end as soon as you have the shots. Other lanes wait while you hold the emulator.
- Look at each shot you take. A proof of a broken screen is a `--fail`, with a note that says what is wrong.
- For a web app, take the screenshots with the browser tool and add them: `kitt proof add shot.png "empty state"`, with `kitt proof begin --no-emulator`.
- Commits after a proof make it stale. Prove again after the last change.

Say in your report that the proof exists and what it shows; the person opens it from the dashboard.
