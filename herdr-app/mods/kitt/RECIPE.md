# kitt

The kitt lane dashboard as a view of the Herdr app's own. A **kitt** button in the tab bar swaps the shown tab's terminals for the lanes, laid out as the TUI's `kitt dash`: the kitt wordmark with three facts beside it, one row per lane (a git worktree per ticket, with its own ports, dev servers and agent), the open pull requests without a lane, and the keys. Every kitt action starts from here; in Herdr a lane is a space, nested under its repo's space.

## What you see

- **Tab bar** (`kitt-tabs`, the app's `tabs` with one button added): after `+`, a **kitt** button (lane icon, 12px) with the `selected` background while the dashboard shows. While it shows, no tab is drawn as active.
- **Header** (24px side padding): the wordmark of `cmd/kitt/logo.go` (`logoWord`, 26×8 pixels, 7px squares, 5px under 700px width): "ki" in the text color, "tt" in `ansi12`. Beside it, 13px, one fact a line: "N lanes · M open PRs without one", "nobody is waiting for you" or "N waiting for you" (blocked color), "emulator free" or "emulator → <lane>" (lane in `ansi6`). Right: repo chips when there are several, then new (plus), backlog (list), refresh (spins while `kitt ls` runs).
- **Lanes**, grouped under the repo name (dim), 26px rows in the terminal font: `▸` on the selected row (`selected` background, name bold), the agent dot (● blocked red, ● done green, spinner working, ○ idle, · none), name (230px, 170px under 760px), `#issue title` or the PR title or the branch (dim, takes the rest; hidden under 640px), touched apps joined by `·` (`ansi6`, 130px, hidden under 880px), agent word (needs you / finished / working / idle / –), git (`±dirty ↓behind`), PR (`#n ✓|✗|●`, `#n merged` in `ansi5`, `#n closed` dim), proof (`proof ✓|✗|stale`, `proving…`, `checks ✓|✗`, `installing…`, `no install·i`), marks: ▲ own dev servers up (green, `working` if only some), ▣ in the emulator, ◆ has a space in Herdr. Worktrees that are not lanes: `· name branch not a lane`, shown with `t`.
- **Below the lanes**: "+ N other worktrees · t shows them", then "open pull requests · enter checks one out as a lane" with `◦ #n title branch #n ✓ draft`.
- **Status line**: a spinner and "… <running action>", else the last result for 30 s (errors in the blocked color).
- **Legend**: every key with its word (`enter` open lane · `e` emulator · `h` phone · `o` browser · `g` agent · `i` install · `u` up · `d` down · `c` check · `p` proof · `n` new · `b` backlog · `a` adopt · `x` remove · `t` all worktrees · `r` refresh), each clickable for the selected row; then "▲ dev servers up · ▣ in the emulator · ◆ has a space in Herdr".
- **Dialogs** (open, new, prompt, backlog, remove, phone): the wordmark at 2px before the title, "Esc" right.

## How it behaves

- The kitt button or `ctrl-shift-j` switches; showing it takes the keyboard (focus is retried at 0/60/180/400 ms until the block is laid out). Picking a tab, `+`, or another space in the sidebar shows the terminals again; so does going into a lane once kitt has opened it.
- Agent status: live from Herdr when the lane's space exists (most urgent of its agents: blocked > done > working > idle), else kitt's `Agent`.
- ↑↓ / j k move, Enter or a double-click opens the lane dialog (the TUI's openings: go to it, a blank Claude ± dev servers, a new Claude, (re)start the servers, Claude on the issue, with a prompt, or told something). `n` new lane (issue number plus apps, or a name, then how it opens), `b` backlog (filter, Enter makes a lane or goes into it), `x` remove (confirm; a refused one offers "Delete anyway"), `c` runs `kitt check` in a tab of the lane's space.
- Polls `kitt ls` every 20 s and after each action, open PRs every 90 s.

## Built from

- Layout `kitt`: the app's `main` with the `main` tab block wrapped in a stack `{ id: "view", type: "stack", active: "main", children: [main, { id: "kitt-dash", type: "block", content: { mod: "kitt" } }] }` and the tab bar block's mod set to `kitt-tabs`. keys.json `layout:kitt`: `"ctrl-shift-j": "kitt-tabs.toggle"`.
- `kitt-tabs`: `layout.select { stack: "view", child }`; a tab click and `+` select `main` first.
- `kitt`: `mod.json` `{ place: "none", focusable: true }`. `props().dialog` makes the same mod a dialog (`openOverlay`); dialogs hand kitt calls to the dashboard through `shared("kitt.requests")`, so long calls survive the dialog closing.
- Programs: `kitt --herdr=app ls --all --json`, `repo list --json`, `backlog --repo r --json`, `focus|new|pr|agent|emu|open|up|down|setup|phone --link|proof open|adopt|rm [--force]`; `gh pr list` / `gh pr view --web`.
- kitt itself makes each lane a space (`space.new { name: lane, folder: worktree }`) after making sure the repo's main checkout has a space; Herdr's `spaces` mod then nests the lane under it.

## Watch out

- `line_height(n)` takes a multiple, not pixels: `line_height(46)` pushes everything off the block.
- `FocusHandle.focus()` cannot run during render; do it from a timer or task, and only once the block is laid out.
- `<mod>.<command>` reaches shown copies only.
- `exec` has no env option: the backend comes from the `--herdr=app` flag.
- An older kitt answers `unknown command "--herdr=app"`: the mod then calls it without the flag for 5 minutes and warns once.

## Done when

- `herdr-app check` passes for `mods/kitt` and `mods/kitt-tabs`; `herdr-app status` shows layout `kitt` and no errors.
- The kitt button shows the lanes of `kitt ls --all --json` with the TUI's columns, and ↓ works without a click.
- Enter on a lane and "go to it" shows its space, nested under the repo's space in the sidebar.
