# kitt

Claude Code mods for my own dev work.

## Install

```
/plugin marketplace add cephalofoil/kitt
/plugin install pr-watch@kitt
/reload-plugins
```

## Mods

### pr-watch

Shown above the prompt, only when there is something to act on:

- the branch's open PR: status, issue title, one chip per check, the failing step and log lines; `1` retry failed, `2` hand the log to Claude, `3` open in browser, `x` hide
- uncommitted work, with `c` to commit and push following the repo's own conventions
- unpushed commits, with `p` to push
- commits behind the default branch and whether a rebase would conflict, with `b` to rebase

The branch name sits at the end of the hint line. `/pr-watch` opens the full view of the PR.

Needs `git` and an authenticated `gh`. Keys work once the band has focus (click it, or ctrl+x tab).

## Develop

```
claude --plugin-dir ./pr-watch
claude plugin validate ./pr-watch
claude plugin test ./pr-watch
```
