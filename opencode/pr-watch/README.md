# pr-watch for opencode

pr-watch for [opencode](https://opencode.ai). Above the prompt it shows:

- the branch's pull request, with its checks and the lines of a failing log;
- uncommitted work;
- how far the branch is behind the default branch.

Keys commit, push and rebase, and the model gets the same steps as tools. It is the opencode side of kitt's Claude Code mod in `pr-watch/`, and it needs `git` and `gh`.

## How it gets to opencode

It comes with kitt, not from a package registry. The kitt binary carries these files (`embed.go`) and writes them where opencode loads plugins from:

- **In kitt's lanes:** by itself. kitt starts opencode with `OPENCODE_CONFIG_DIR` set to its own folder, so pr-watch is there without an install.
- **Everywhere:** `kitt opencode install` writes it into `~/.config/opencode/kitt/` and lists it in `opencode.json` (the tools) and `tui.json` (the band and keys). `kitt opencode uninstall` takes it out again.
- **In this repo:** `.opencode/` loads it from here, for working on it.

Options, set where `tui.json` lists it:

```json
{ "plugin": [["./kitt/pr-watch/tui.tsx", { "autoRebase": true, "keybinds": { "prwatch.push": "<leader>u" } }]] }
```

- `autoRebase` (default `false`): a branch with an open PR that falls behind is rebased and pushed without a key press, as long as the tree is clean and the rebase has no conflicts.
- `keybinds`: replace the default keys, which follow opencode's leader key (`ctrl+x`):

| Key | What it does |
| --- | --- |
| `<leader>w` | opens the full view of the PR |
| `<leader>i` | commit |
| `<leader>p` | push, and open a PR when there is none |
| `<leader>o` | rebase onto the default branch, then push |
| `<leader>f` | hand a failed step to the agent |
| `<leader>d` | cancel |

## Tools

- `pr_watch_commit` `{ subject, body?, paths? }`: stages and commits. It refuses on the default branch and never pushes.
- `pr_watch_push` `{ force_with_lease? }`: pushes. A refused push answers with the commits the remote holds.
- `pr_watch_rebase_and_push`: rebases onto the default branch and pushes with lease. A rebase with conflicts is undone, and the answer names the files.

**Merging is yours.** No key and no tool merges. `gh pr merge` and the API's merge call go through opencode's permission prompt. That prompt offers "always", so this guard is weaker than the Claude Code mod's.

## Development

The parsing and the types are the Claude Code mod's (`pr-watch/hooks/parse.ts`, `pr-watch/types/`), and `shared/` holds a copy so the binary can carry it. Run `bun run sync` after changing them there; a test fails when the copy drifts. `bun run typecheck` and `bun test` check the plugin. When the files the binary carries change, `embed.go` lists them.
