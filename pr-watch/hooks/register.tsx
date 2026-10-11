import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { PrWatchCheck, PrWatchGit, PrWatchIssue, PrWatchLog, PrWatchSnapshot } from '../types'
import { countBoxes, duration, headline, issueNumber, overall, parseChecks, parseLog } from './parse'
import type { Overall } from './parse'

const PANE = 'pr-watch'
const FIELDS =
  'number,title,body,state,isDraft,headRefOid,headRefName,closingIssuesReferences,statusCheckRollup'
const TICK_MS = 15_000
const IDLE_TICKS = 4
const FETCH_TICKS = 20

// The tools Claude calls to act on the branch: each does fixed steps and answers
// with what happened. A key of the band runs the same steps itself, without a
// turn; the one thing it needs a model for, a commit message or a PR's text, it
// asks of a fork of the session. When a step does not go through, the band says
// so in one line with two keys: resolve it with Claude, or cancel. Only the first
// gives Claude a turn.
const TOOL = {
  commit: 'mcp__pr-watch__commit',
  push: 'mcp__pr-watch__push',
  rebase: 'mcp__pr-watch__rebase_and_push',
} as const

// What a key sends is a few words the person reads in the transcript. How to do
// it rides along as context only the model reads.
const ASK = {
  commit: 'Commit my changes.',
  ship: 'Push this branch and open a PR.',
  push: 'Push this branch.',
  rebase: 'Rebase this branch onto ',
} as const

const HOW = {
  commit:
    `Sent by the Commit key of the pr-watch band. Look at what is uncommitted, write the message this repo asks for (AGENTS.md, ` +
    `CLAUDE.md, the subjects in \`git log\`, the issue the branch works on), and call the tool ${TOOL.commit} with it. For ` +
    `clearly unrelated changes, call it once per group with \`paths\`. Do not run \`git commit\` yourself, do not push, do not open a PR. ` +
    `If something should not be committed (a secret, generated junk, work that is visibly broken), do not call the tool: say what and why. ` +
    `Answer with the commit subject and nothing else.`,
  ship:
    `Sent by the Push & open PR key of the pr-watch band. First run the repo's own checks for what changed: \`kitt check\` when the ` +
    `repo has a kitt.toml, otherwise the checks AGENTS.md or CLAUDE.md name. If a check fails, stop and report it. If the branch is ` +
    `behind the default branch, call ${TOOL.rebase} first. Then call ${TOOL.push}, and open a PR against the default branch with ` +
    `\`gh pr create\`, following the repo's PR conventions: title style, the sections its instructions ask for, the issue it closes. ` +
    `If \`kitt proof status\` shows a proof for this lane, say in the PR body what it shows. Do not merge the PR: merging is the person's decision. Answer with the PR link and one line on the checks.`,
  push:
    `Sent by the Push key of the pr-watch band. Call the tool ${TOOL.push}. If it answers that the remote holds other commits, ` +
    `read them: call again with force_with_lease only when they are this branch's own commits from before a rebase; if they are ` +
    `someone else's work, stop and say so. Answer in one line.`,
  rebase:
    `Sent by the Rebase key of the pr-watch band. It means: rebase this branch onto the default branch, then push this branch to ` +
    `its own remote branch, so a PR can be opened or updated. Nothing is pushed to the default branch. Call the tool ${TOOL.rebase}. ` +
    `If it answers that the rebase conflicts, rebase by hand: resolve each conflict only where the right result is clear from both ` +
    `sides, and where it is not, stop with the rebase still open and ask, naming the file and the two versions. After a rebase by ` +
    `hand, call ${TOOL.push} with force_with_lease. Answer in one or two lines.`,
} as const

// What a key asks of a fork of the session: text alone, or "ASK:" to hand the key to Claude.
const WRITE = {
  commit:
    `The person pressed the Commit key of the pr-watch band. Write the commit message for the uncommitted changes below, in the form ` +
    `this repo asks for (its instructions, the subjects in the log, the issue the branch works on). Answer with the message alone: the ` +
    `subject on the first line, then an empty line and a body only where the repo's form asks for one. No quotes, no code fence. ` +
    `If something should not be committed as it is (a secret, generated junk, work that is visibly broken), or the changes are clearly ` +
    `unrelated and belong in separate commits, answer instead with one line starting "ASK: " and the reason.`,
  pr:
    `The person pressed the Push & open PR key of the pr-watch band. The branch is pushed. Write the pull request ` +
    `for the commits below, following the repo's PR conventions: title style, the sections its instructions ask for, the issue it closes. ` +
    `Where a proof is given, say in the body what it shows. Answer with the title on the first line, an empty line, then the body in ` +
    `Markdown. No quotes, no code fence around the whole.`,
} as const
// How much of a diff a model is shown, and how many changed files a commit by key may have.
const MOST = 200_000
const FILES = 1000

// What Claude reads beside a key's few words when the key tried first and the person had it resolved.
const TRIED =
  `The key ran its fixed steps itself first, without a turn; a step failed, and the person chose to have you do it. Why it failed ` +
  `follows. Say in one line what went wrong, then do what the key was doing, leaving out what already went through. Ask first only ` +
  `where the decision is the person's: a secret or junk among the changes, someone else's commits on the remote. Keep the answer ` +
  `to a few lines.`

/** What the Rebase key sends: the branch goes onto the base, then the branch itself is pushed. */
const rebaseAsk = (base: string): string => `${ASK.rebase}${base.replace(/^origin\//, '')} and push the branch.`

function howFor(asked: string): string | null {
  if (asked === ASK.commit) return HOW.commit
  if (asked === ASK.ship) return HOW.ship
  if (asked === ASK.push) return HOW.push
  if (asked.startsWith(ASK.rebase)) return HOW.rebase

  return null
}

const snapshot = atom({ plugin: 'pr-watch', key: 'snapshot' } as const, null)
const gitState = atom({ plugin: 'pr-watch', key: 'git' } as const, null)
const hiddenPr = atom({ plugin: 'pr-watch', key: 'hiddenPr' } as const, null)
const hiddenRebase = atom({ plugin: 'pr-watch', key: 'hiddenRebase' } as const, null)
const isLoaded = atom({ plugin: 'pr-watch', key: 'isLoaded' } as const, false)
const doing = atom({ plugin: 'pr-watch', key: 'doing' } as const, null)
const halted = atom({ plugin: 'pr-watch', key: 'halted' } as const, null)

const GLYPH = { pass: '✓', fail: '✗', running: '●', queued: '○', skipped: '–' } as const
const COLOR = { pass: 'green', fail: 'red', running: 'yellow', queued: 'gray', skipped: 'gray' } as const
const HEAD = {
  failed: { color: 'red', mark: '✗', word: 'checks failing' },
  running: { color: 'yellow', mark: '●', word: 'checks running' },
  green: { color: 'green', mark: '✓', word: 'all checks green' },
  merged: { color: 'magenta', mark: '✓', word: 'merged' },
  closed: { color: 'gray', mark: '–', word: 'closed' },
} as const

// A command that merges a pull request: the CLI's own, or the API's merge endpoint.
const MERGE = /\bgh\s+pr\s+merge\b|\bgh\s+api\b[^\n]*\/pulls\/\d+\/merge\b/
const NO_MERGE =
  'Merging a pull request is the person\'s decision, and they did not approve this one. Do not merge and do not try another way: ' +
  'say that the PR is ready to merge, and stop.'

// Set from the plugin's options: rebase a PR's branch by itself when it falls behind.
let isAutoRebase = false
let autoFor = ''

let isPrBusy = false
let isGitBusy = false
let ticks = 0
let last: { key: string; overall: Overall } | null = null
let issue: (PrWatchIssue & { at: number }) | null = null
let base: string | null = null
let tried: { key: string; conflicts: string[] | null } | null = null
// The main loop's turns under way: git is not moved under a turn that is working.
const running = new Set<string>()
let isActing = false
// Why a key's own steps stopped: it rides along with the few words Claude is then sent.
let stopped: string | null = null
const logs = new Map<string, PrWatchLog>()

const keyOf = (pr: PrWatchSnapshot): string => `${pr.number}:${pr.headSha}`

const plural = (count: number, one: string): string => `${count} ${one}${count === 1 ? '' : 's'}`

async function run($: EngineInterface, argv: readonly string[], timeoutMs = 20_000) {
  try {
    return await $.process.run(argv, { timeoutMs })
  } catch {
    return null
  }
}

async function text($: EngineInterface, argv: readonly string[]): Promise<string | null> {
  const ran = await run($, argv)

  return ran === null || ran.exitCode !== 0 ? null : ran.stdout.trim()
}

// --- The branch: what is uncommitted, unpushed, behind ----------------------

/** The remote's default branch (`origin/main`), asked once: origin's HEAD, else main, else master. */
async function findBase($: EngineInterface): Promise<string | null> {
  if (base !== null) return base

  const head = await text($, ['git', 'symbolic-ref', '--short', 'refs/remotes/origin/HEAD'])

  if (head !== null && head !== '') {
    base = head

    return base
  }
  for (const name of ['origin/main', 'origin/master']) {
    if ((await text($, ['git', 'rev-parse', '--verify', '--quiet', name])) !== null) {
      base = name

      return base
    }
  }

  return null
}

async function loadGit($: EngineInterface): Promise<PrWatchGit | null> {
  const branch = await text($, ['git', 'branch', '--show-current'])

  if (branch === null || branch === '') return null

  const onto = await findBase($)
  const status = (await run($, ['git', 'status', '--porcelain']))?.stdout.split(/\r?\n/).filter(Boolean) ?? []
  const untracked = status.filter(line => line.startsWith('??')).length
  const counts =
    (onto === null ? null : await text($, ['git', 'rev-list', '--left-right', '--count', `HEAD...${onto}`])) ?? '0 0'
  const [ahead = 0, behind = 0] = counts.split(/\s+/).map(Number)
  const unpushed = await text($, ['git', 'rev-list', '--count', '@{upstream}..HEAD'])
  const baseSha = (onto === null ? null : await text($, ['git', 'rev-parse', onto])) ?? ''
  const head = (await text($, ['git', 'rev-parse', 'HEAD'])) ?? ''
  const key = `${head}:${baseSha}`

  if (behind === 0) tried = { key, conflicts: [] }
  if (onto !== null && behind > 0 && tried?.key !== key) {
    // Merges the two tips in memory: nothing in the work tree or the index moves.
    const merged = await run($, ['git', 'merge-tree', '--write-tree', '--name-only', 'HEAD', onto])
    const names = merged?.stdout.split(/\r?\n\r?\n/)[0]?.split(/\r?\n/).slice(1).filter(Boolean) ?? []

    tried = { key, conflicts: merged === null || merged.exitCode > 1 ? null : merged.exitCode === 0 ? [] : names }
  }

  return {
    branch,
    head,
    modified: status.length - untracked,
    untracked,
    behind,
    ahead,
    unpushed: unpushed === null ? null : Number(unpushed),
    conflicts: tried?.conflicts ?? null,
    base: onto ?? '',
    baseSha,
  }
}

async function refreshGit($: EngineInterface) {
  if (isGitBusy) return

  isGitBusy = true

  try {
    const now = await loadGit($)

    await update($, gitState, () => now)

    if (isAutoRebase && now !== null && now.behind > 0 && now.modified + now.untracked === 0 && now.conflicts?.length === 0) {
      const pr = await read($, snapshot)
      const key = `${now.head}:${now.baseSha}`

      if (pr !== null && pr.state === 'OPEN' && pr.branch === now.branch && autoFor !== key && !isActing && running.size === 0) {
        autoFor = key
        $.ui.toast(`${now.behind} behind ${now.base.replace(/^origin\//, '')}: rebasing (auto-rebase is on)`, { timeoutMs: 8000 })
        void act($, 'Auto-rebase ran', () => rebaseTool($), rebaseAsk(now.base))
      }
    }
  } catch (error) {
    $.ui.log(`pr-watch: ${error instanceof Error ? error.message : String(error)}`)
  } finally {
    isGitBusy = false
  }
}

// --- The pull request: checks, failing log, issue ---------------------------

async function loadIssue($: EngineInterface, number: number, now: number) {
  if (issue !== null && issue.number === number && now - issue.at < 300_000) return issue

  const ran = await text($, ['gh', 'issue', 'view', String(number), '--json', 'title,body'])

  if (ran === null) return issue?.number === number ? issue : null

  const data = JSON.parse(ran) as { title?: string; body?: string }
  issue = { number, title: data.title ?? '', ...countBoxes(data.body ?? ''), at: now }

  return issue
}

async function loadLog($: EngineInterface, check: PrWatchCheck) {
  if (check.jobId === null) return null

  const kept = logs.get(check.jobId)

  if (kept !== undefined) return kept

  const ran = await run($, ['gh', 'run', 'view', '--job', check.jobId, '--log-failed'], 45_000)

  // gh has no log until the whole run is over: ask again at the next poll.
  if (ran === null || ran.exitCode !== 0 || ran.stdout.trim() === '') return null

  const log = { jobId: check.jobId, ...parseLog(ran.stdout) }
  logs.set(check.jobId, log)

  return log
}

async function loadPr($: EngineInterface): Promise<PrWatchSnapshot | null> {
  const ran = await text($, ['gh', 'pr', 'view', '--json', FIELDS])

  if (ran === null) return null

  const data = JSON.parse(ran) as {
    number: number
    title: string
    body?: string
    state: PrWatchSnapshot['state']
    isDraft?: boolean
    headRefOid: string
    headRefName: string
    closingIssuesReferences?: { number?: number }[]
    statusCheckRollup?: Parameters<typeof parseChecks>[0]
  }
  const now = await $.clock.now()
  const checks = parseChecks(data.statusCheckRollup ?? [], now)
  const found = issueNumber(data.closingIssuesReferences, data.title, data.body ?? '')
  const failed: PrWatchLog[] = []

  for (const check of checks.filter(one => one.state === 'fail')) {
    const log = await loadLog($, check)

    if (log !== null) failed.push(log)
  }

  return {
    number: data.number,
    title: data.title,
    state: data.state,
    isDraft: data.isDraft === true,
    headSha: data.headRefOid,
    branch: data.headRefName,
    issue: found === null ? null : await loadIssue($, found, now),
    checks,
    logs: failed,
  }
}

function summary(pr: PrWatchSnapshot): string {
  const now = overall(pr)
  const bad = pr.checks.filter(check => check.state === 'fail').map(check => check.name)

  return now === 'failed'
    ? `PR #${pr.number} ✗ ${bad[0]}${bad.length > 1 ? ` +${bad.length - 1}` : ''}`
    : `PR #${pr.number} ${HEAD[now].mark} ${HEAD[now].word}`
}

async function refreshPr($: EngineInterface) {
  if (isPrBusy) return

  isPrBusy = true

  try {
    const pr = await loadPr($)
    const now = pr === null ? null : overall(pr)
    const before = pr !== null && last !== null && last.key === keyOf(pr) ? last.overall : null

    await update($, snapshot, () => pr)
    await update($, isLoaded, () => true)
    last = pr === null || now === null ? null : { key: keyOf(pr), overall: now }

    if (pr !== null && before !== null && before !== now && now !== 'running' && now !== 'closed') {
      $.ui.toast(summary(pr), { timeoutMs: 8000 })
    }
  } catch (error) {
    $.ui.log(`pr-watch: ${error instanceof Error ? error.message : String(error)}`)
  } finally {
    isPrBusy = false
  }
}

async function retry($: EngineInterface, pr: PrWatchSnapshot) {
  const failed = pr.checks.filter(check => check.state === 'fail')
  const runs = [...new Set(failed.map(check => check.runId).filter((id): id is string => id !== null))]

  for (const id of runs) {
    const ran = await run($, ['gh', 'run', 'rerun', id, '--failed'], 30_000)

    $.ui.toast(
      ran?.exitCode === 0
        ? `PR #${pr.number}: failed jobs restarted`
        : `Retry refused: ${(ran?.stderr ?? 'gh did not answer').trim().split('\n')[0]}`,
      { timeoutMs: 8000 },
    )
  }
  for (const check of failed) if (check.jobId !== null) logs.delete(check.jobId)

  await refreshPr($)
}

async function hand($: EngineInterface, pr: PrWatchSnapshot) {
  const parts = pr.checks
    .filter(check => check.state === 'fail')
    .map(check => {
      const log = pr.logs.find(one => one.jobId === check.jobId)

      return log === undefined
        ? `Job "${check.name}" failed; its log is not available yet.`
        : `Job "${check.name}" failed in step "${log.step}". Log tail:\n\`\`\`\n${log.lines.join('\n')}\n\`\`\``
    })

  await $.prompt.submit({
    text: `CI on PR #${pr.number} (${pr.branch}) is failing.\n\n${parts.join('\n\n')}\n\nFind the cause and fix it.`,
    asUser: true,
  })
}

async function browse($: EngineInterface, pr: PrWatchSnapshot) {
  await run($, ['gh', 'pr', 'view', String(pr.number), '--web'])
}

const why = (ran: { stderr: string; stdout: string } | null): string =>
  ((ran?.stderr || ran?.stdout) ?? 'git did not answer').trim().split('\n').filter(Boolean).pop() ?? ''

const tail = (ran: { stderr: string; stdout: string } | null, most: number): string =>
  `${ran?.stdout ?? ''}\n${ran?.stderr ?? ''}`.split(/\r?\n/).filter(line => line.trim() !== '').slice(-most).join('\n')

/** Tells Claude what a key did to the branch, without starting a turn. */
async function tell($: EngineInterface, what: string) {
  try {
    await $.session.append({ message: { type: 'user', content: [{ type: 'text', text: `[pr-watch] ${what}` }] } })
  } catch {
    // A session that takes no notes still has the toast.
  }
}

// --- The tools: fixed steps, an answer Claude reads ---------------------------

/** `isDone` marks a step that went through: a key reads it, Claude never sees it. */
type Answer = { result: string; isDone?: true } | { deny: string }

const answerOf = (out: Answer): Answer => ('deny' in out ? out : { result: out.result })

/** A step failed: the band says so in one line, with a key to resolve it with Claude and one to cancel. */
async function stop($: EngineInterface, key: string, why: string, text: string) {
  await update($, doing, () => null)
  await update($, halted, () => ({ key, why, text }))
}

/** The person chose Claude: the key's own few words are sent, the reason rides along unseen. */
async function resolve($: EngineInterface, halt: { why: string; text: string }) {
  stopped = halt.why
  await update($, halted, () => null)
  void $.prompt.submit({ text: halt.text, asUser: true })
}

/**
 * Runs a tool's fixed steps for a key, without a turn, and tells Claude what
 * was done. `text` is what the key asks of Claude when a step fails and the
 * person has it resolved.
 */
async function act($: EngineInterface, said: string, step: () => Promise<Answer>, text: string) {
  if (isActing) return

  const key = said.replace(/^The person pressed | ran$/g, '')

  if (running.size > 0) {
    $.ui.toast(`${key} waits: Claude is working. Press it again when the turn has ended.`, { timeoutMs: 8000 })

    return
  }

  isActing = true
  await update($, halted, () => null)
  await update($, doing, () => key)

  try {
    const out = await step()

    if ('deny' in out) {
      $.ui.toast(out.deny, { timeoutMs: 8000 })
    } else if (out.isDone === true) {
      $.ui.toast(out.result, { timeoutMs: 8000 })
      await tell($, `${said}: ${out.result}`)
    } else {
      await stop($, key, out.result, text)
    }
  } catch (error) {
    // A step that threw is a step that did not go through.
    await stop($, key, error instanceof Error ? error.message : String(error), text)
  } finally {
    isActing = false
    await update($, doing, () => null)
  }
}

/** The default branch is never committed to, pushed or rebased by a tool. */
async function branchFor($: EngineInterface): Promise<PrWatchGit | { deny: string }> {
  const git = await loadGit($)

  if (git === null) return { deny: 'This directory is not on a git branch.' }
  if (`origin/${git.branch}` === git.base) {
    return { deny: `${git.branch} is the default branch. Create a branch first (git switch -c <type>/<name>), then call the tool again.` }
  }

  return git
}

async function commitTool($: EngineInterface, input: { subject?: unknown; body?: unknown; paths?: unknown }): Promise<Answer> {
  const git = await branchFor($)

  if ('deny' in git) return git

  const subject = typeof input.subject === 'string' ? input.subject.trim() : ''
  const body = typeof input.body === 'string' ? input.body.trim() : ''
  const paths = Array.isArray(input.paths) ? input.paths.filter((path): path is string => typeof path === 'string') : []

  if (subject === '') return { deny: 'A commit needs a subject.' }

  const added = await run($, paths.length > 0 ? ['git', 'add', '--', ...paths] : ['git', 'add', '-A'], 60_000)

  if (added?.exitCode !== 0) return { result: `git add failed: ${why(added)}\nNothing was committed.` }

  const staged = await run($, ['git', 'diff', '--cached', '--quiet'])

  if (staged?.exitCode === 0) return { result: 'There is nothing to commit for these paths.' }

  // A repo's commit hooks run here and may take their time.
  const committed = await run($, ['git', 'commit', '-m', subject, ...(body === '' ? [] : ['-m', body])], 300_000)

  if (committed?.exitCode !== 0) {
    return { result: `git refused the commit.\nThe changes stay staged. Output:\n${tail(committed, 30)}` }
  }

  const sha = (await text($, ['git', 'rev-parse', '--short', 'HEAD'])) ?? ''
  const left = (await run($, ['git', 'status', '--porcelain']))?.stdout.split(/\r?\n/).filter(Boolean).length ?? 0

  await refreshGit($)

  return {
    result: `Committed ${sha} ${subject}. ${left === 0 ? 'Nothing is left uncommitted.' : `${left} files are still uncommitted.`} Not pushed.`,
    isDone: true,
  }
}

// The remote head Claude was shown when a push was refused: a forced push is
// leased to exactly that commit, so it cannot overwrite anything newer.
let shown: { branch: string; sha: string } | null = null

async function pushTool($: EngineInterface, input: { force_with_lease?: unknown }): Promise<Answer> {
  const git = await branchFor($)

  if ('deny' in git) return git

  const isForced = input.force_with_lease === true
  const upstream = await text($, ['git', 'rev-parse', '--abbrev-ref', '@{upstream}'])
  const lease = shown !== null && shown.branch === git.branch ? `--force-with-lease=${git.branch}:${shown.sha}` : '--force-with-lease'
  const argv = isForced
    ? ['git', 'push', lease, 'origin', git.branch]
    : upstream === null
      ? ['git', 'push', '--set-upstream', 'origin', git.branch]
      : ['git', 'push']
  const pushed = await run($, argv, 180_000)

  if (pushed?.exitCode === 0) {
    shown = null
    await refreshGit($)
    await refreshPr($)

    return { result: `Pushed ${git.branch} to origin${isForced ? ' with --force-with-lease' : ''}.`, isDone: true }
  }
  if (isForced || !/rejected|non-fast-forward|fetch first|stale info/.test(`${pushed?.stderr}`)) {
    return { result: `The push failed: ${why(pushed)}` }
  }

  // Refused: show what the remote holds that the local branch lacks.
  await run($, ['git', 'fetch', '--quiet', 'origin', git.branch], 30_000)

  const sha = (await text($, ['git', 'rev-parse', `origin/${git.branch}`])) ?? ''
  const theirs = (await text($, ['git', 'log', '--format=%h %an: %s', '-10', `HEAD..origin/${git.branch}`])) ?? ''

  shown = { branch: git.branch, sha }

  return {
    result:
      `The remote branch has commits this one lacks.\norigin/${git.branch} holds:\n${theirs}\n` +
      `If these are this branch's own commits from before a rebase, call the tool again with force_with_lease: true. ` +
      `If any is someone else's work, do not force: stop and tell the person.`,
  }
}

async function rebaseTool($: EngineInterface): Promise<Answer> {
  const git = await branchFor($)

  if ('deny' in git) return git
  if (git.modified + git.untracked > 0) {
    return { deny: `There are uncommitted changes. Commit them first (${TOOL.commit}), then call this tool again.` }
  }

  const onto = git.base.replace(/^origin\//, '')

  await run($, ['git', 'fetch', '--quiet', 'origin', onto], 30_000)

  const behind = await text($, ['git', 'rev-list', '--count', `HEAD..${git.base}`])

  const isRebased = behind !== '0'

  const rebased = isRebased ? await run($, ['git', 'rebase', git.base], 180_000) : null

  if (isRebased && rebased?.exitCode !== 0) {
    const files = (await text($, ['git', 'diff', '--name-only', '--diff-filter=U'])) ?? ''

    // Back to exactly where the branch was: nothing half-done is left behind.
    await run($, ['git', 'rebase', '--abort'])
    // Merging the two tips can be clean where replaying commit by commit is not: what the rebase hit is what is known now.
    tried = { key: `${git.head}:${(await text($, ['git', 'rev-parse', git.base])) ?? ''}`, conflicts: files.split(/\r?\n/).filter(Boolean) }
    await refreshGit($)

    return {
      result:
        `The rebase onto ${git.base} has conflicts.\nIt was undone and the branch is as before.\n` +
        (files === '' ? `git said: ${why(rebased)}` : `Conflicts in:\n${files}`),
    }
  }

  // The branch itself is pushed, to its own remote branch: a first push sets the
  // upstream, a later one is leased, so it refuses when the remote branch moved
  // since it was last seen.
  const upstream = await text($, ['git', 'rev-parse', '--abbrev-ref', '@{upstream}'])
  const pushed = await run(
    $,
    upstream === null ? ['git', 'push', '--set-upstream', 'origin', git.branch] : ['git', 'push', '--force-with-lease'],
    180_000,
  )
  const did = isRebased ? `Rebased ${git.branch} onto ${git.base}` : `${git.branch} was already on top of ${git.base}`
  const outcome =
    pushed?.exitCode === 0
      ? `${did} and pushed it to origin/${git.branch}${isRebased && upstream !== null ? ' with --force-with-lease; its commits have new ids' : ''}. A PR can be opened from it now.`
      : `The push was refused: ${why(pushed)}\n${did}; only the push is missing.`
  await refreshGit($)
  await refreshPr($)

  return pushed?.exitCode === 0 ? { result: outcome, isDone: true } : { result: outcome }
}

// --- The keys: the same steps without a turn, the text from a fork ------------

/** The repo's own instructions, for a model that has not read the session. */
async function instructions($: EngineInterface): Promise<string> {
  const found: string[] = []

  for (const name of ['AGENTS.md', 'CLAUDE.md']) {
    try {
      found.push(`${name}:\n${String(await $.fs.read(name)).slice(0, 20_000)}`)
    } catch {
      // A repo may have neither.
    }
  }

  return found.join('\n\n')
}

/**
 * Asks a fork of the session for text: its first line and the rest, or why it
 * was not written. A session with no answer yet has nothing to fork, so a
 * model on its own writes it, from the repo's instructions.
 */
async function write($: EngineInterface, what: string, prompt: string): Promise<{ head: string; rest: string } | { result: string }> {
  const forked = await $.model.fork({ prompt })
  const reply =
    !forked.isAnswered && forked.reason === 'nothing-to-fork'
      ? await $.model.complete({ model: 'sonnet', prompt, system: await instructions($), maxTokens: 2000 })
      : forked

  if (!reply.isAnswered) return { result: `The ${what} could not be written (${reply.reason}).` }

  const said = reply.text.trim().replace(/^```\w*\r?\n|\r?\n```$/g, '').trim()
  const [head = '', ...rest] = said.split(/\r?\n/)

  if (head.startsWith('ASK:')) return { result: said.slice(4).trim() }
  if (head === '' || head.length > 200) return { result: `The ${what} came back without a one-line subject.` }

  return { head: head.trim(), rest: rest.join('\n').trim() }
}

/**
 * The uncommitted work as text: every changed file by name and size of change,
 * then the diff, new files whole. A long diff is cut and says so: the list of
 * files stays complete, and the fork knows from the session what was done.
 */
async function changes($: EngineInterface): Promise<{ shown: string; files: number }> {
  const status = ((await text($, ['git', 'status', '--porcelain'])) ?? '').split(/\r?\n/).filter(Boolean)
  const log = (await text($, ['git', 'log', '-8', '--format=%s'])) ?? ''
  const stat = (await text($, ['git', 'diff', 'HEAD', '--stat'])) ?? ''
  const diff = (await run($, ['git', 'diff', 'HEAD']))?.stdout ?? ''
  const fresh = ((await text($, ['git', 'ls-files', '--others', '--exclude-standard'])) ?? '').split(/\r?\n/).filter(Boolean)
  let all = diff

  for (const path of fresh) {
    if (all.length > MOST) break

    all = `${all}\n${(await run($, ['git', 'diff', '--no-index', '--', '/dev/null', path]))?.stdout ?? ''}`
  }

  return {
    files: status.length,
    shown:
      `Last subjects:\n${log}\n\ngit status --porcelain:\n${status.join('\n')}\n\nNew files:\n${fresh.join('\n')}\n\n` +
      `git diff --stat:\n${stat}\n\nDiff:\n${all.slice(0, MOST)}` +
      (all.length > MOST ? '\n(The diff is cut here. The lists above name every changed file.)' : ''),
  }
}

async function commitKey($: EngineInterface): Promise<Answer> {
  const git = await branchFor($)

  if ('deny' in git) return { result: git.deny.replace('. ', '.\n') }

  const work = await changes($)

  // Thousands of files are not a change someone made by hand: a folder that wants ignoring, more likely.
  if (work.files > FILES) {
    return { result: `${work.files} files are uncommitted: too many for one commit.\nThat looks like generated files or a folder that should be ignored. Nothing was committed.` }
  }

  const message = await write($, 'commit message', `${WRITE.commit}\n\n${work.shown}`)

  return 'result' in message ? message : commitTool($, { subject: message.head, body: message.rest })
}

/** Runs `kitt check`, the band showing the line it last wrote; stopped after ten minutes. */
async function check($: EngineInterface): Promise<{ exitCode: number | null; stdout: string; stderr: string } | null> {
  const child = $.process.spawn({ argv: ['kitt', 'check'] })
  const limit = $.clock.after(600_000, () => void child.return(undefined as never))
  let stdout = ''

  try {
    for (;;) {
      const piece = await child.next()

      if (piece.done === true) return { exitCode: piece.value?.code ?? null, stdout, stderr: '' }

      stdout = `${stdout}${piece.value.text.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '')}`.slice(-20_000)

      const line = stdout.split(/[\r\n]+/).filter(one => one.trim() !== '').pop() ?? ''

      await update($, doing, () => `kitt check · ${line.trim().slice(0, 90)}`)
    }
  } catch {
    return null
  } finally {
    limit.cancel()
  }
}

/** Why `gh pr create` refused, in a few words; gh's own last line where the cause is not a known one. */
function ghSaid(ran: { stderr: string; stdout: string } | null): string {
  const said = `${ran?.stderr ?? ''}${ran?.stdout ?? ''}`

  if (ran === null) return 'gh did not answer.'
  if (/known GitHub host|not a git repository|no git remotes/i.test(said)) return 'this repo has no remote on GitHub.'
  if (/already exists/i.test(said)) return 'this branch already has a PR.'
  if (/gh auth login|authentication|HTTP 401/i.test(said)) return 'gh is not logged in.'
  if (/No commits between/i.test(said)) return 'the branch has no commits the default branch lacks.'

  return why(ran)
}

async function shipKey($: EngineInterface): Promise<Answer> {
  const git = await branchFor($)

  if ('deny' in git) return { result: git.deny.replace('. ', '.\n') }

  let hasKitt = true

  try {
    await $.fs.read('kitt.toml')
  } catch {
    hasKitt = false
  }
  // A repo without a kitt.toml names no checks a key could run: the PR is opened all the same, and says so.
  const checked = hasKitt ? await check($) : { exitCode: 0, stdout: '', stderr: '' }
  const checks = hasKitt ? 'kitt check passed' : 'no checks were run (this repo has no kitt.toml)'

  if (checked === null) return { result: '`kitt check` could not be started.\nNothing was pushed.' }
  if (checked.exitCode !== 0) return { result: `\`kitt check\` failed.\nNothing was pushed. Output:\n${tail(checked, 40)}` }
  if (git.behind > 0 && git.conflicts?.length !== 0) {
    return { result: `The branch is ${git.behind} behind ${git.base} and the rebase has conflicts.\nNothing was pushed (${checks}).` }
  }

  await update($, doing, () => (git.behind > 0 ? 'rebasing and pushing' : 'pushing'))

  const pushed = git.behind > 0 ? await rebaseTool($) : await pushTool($, {})

  if ('deny' in pushed) return { result: pushed.deny }
  if (pushed.isDone !== true) return { result: pushed.result }

  const onto = git.base.replace(/^origin\//, '')
  const commits = (await text($, ['git', 'log', '--format=%h %s%n%b', `${git.base}..HEAD`])) ?? ''
  const stat = (await text($, ['git', 'diff', '--stat', `${git.base}...HEAD`])) ?? ''
  const proof = await text($, ['kitt', 'proof', 'status'])

  await update($, doing, () => 'pushed · writing the PR')
  const pr = await write(
    $,
    'PR text',
    `${WRITE.pr}\n\nBranch ${git.branch} onto ${onto}.\n\nChecks: ${checks}. Say of the checks only this.\n\nCommits:\n${commits.slice(0, MOST)}\n\nFiles:\n${stat}` +
      (proof === null || proof === '' ? '' : `\n\nkitt proof status:\n${proof}`),
  )

  if ('result' in pr) return { result: `${pr.result}\nThe branch is pushed (${checks}); no PR was opened.` }

  const opened = await run($, ['gh', 'pr', 'create', '--base', onto, '--head', git.branch, '--title', pr.head, '--body', pr.rest], 60_000)

  await refreshPr($)

  return opened?.exitCode === 0
    ? { result: `Pushed and PR opened, ${checks}: ${opened.stdout.trim().split(/\r?\n/).pop() ?? ''}`, isDone: true }
    : { result: `The PR could not be opened: ${ghSaid(opened)}\nThe branch is pushed (${checks}). gh said: ${why(opened)}` }
}

/** Leaves a merged branch for the default one, brought up to the remote's tip. */
async function switchToBase($: EngineInterface, git: PrWatchGit) {
  const name = git.base.replace(/^origin\//, '')
  const why = (ran: { stderr: string } | null) => (ran?.stderr ?? 'git did not answer').trim().split('\n')[0]

  await run($, ['git', 'fetch', '--quiet', 'origin', name], 30_000)

  const moved = await run($, ['git', 'switch', name])

  if (moved?.exitCode !== 0) {
    $.ui.toast(`Switch refused: ${why(moved)}`, { timeoutMs: 8000 })

    return
  }

  const pulled = await run($, ['git', 'merge', '--ff-only', git.base])

  $.ui.toast(
    pulled?.exitCode === 0 ? `On ${name}, level with ${git.base}` : `On ${name}, not fast-forwarded: ${why(pulled)}`,
    { timeoutMs: 8000 },
  )
  await tell($, `The person pressed Switch: this checkout is now on ${name}, level with ${git.base}. The branch ${git.branch} is merged and done.`)
  await refreshGit($)
  await refreshPr($)
}

export const register: Register = (on, options) => {
  isAutoRebase = options.autoRebase === true

  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'pr-watch',
      description: "Open the full view of this branch's PR: every check, failing log lines, issue",
    })
    // An earlier version pinned the branch here; a pin outlives a reload until cleared.
    $.ui.status(undefined)
    try {
      await $.tool.register({
        name: 'commit',
        description:
          'Commit uncommitted changes of the current branch with the message you give. Stages everything, or only `paths` when given. ' +
          'Refuses on the default branch. Never pushes. Use it when the person asks to commit.',
        inputSchema: {
          type: 'object',
          properties: {
            subject: { type: 'string', description: 'The commit subject, in the form this repo uses' },
            body: { type: 'string', description: 'Optional commit body' },
            paths: { type: 'array', items: { type: 'string' }, description: 'Only these paths; leave out to commit everything' },
          },
          required: ['subject'],
        },
      })
      await $.tool.register({
        name: 'push',
        description:
          'Push the current branch to origin, setting its upstream on the first push. A refused push answers with the commits the ' +
          'remote holds; force_with_lease then overwrites exactly those, and only those. Refuses on the default branch.',
        inputSchema: {
          type: 'object',
          properties: {
            force_with_lease: {
              type: 'boolean',
              description: "Only after a refused push whose listed commits are this branch's own from before a rebase",
            },
          },
        },
      })
      await $.tool.register({
        name: 'rebase_and_push',
        description:
          'Rebase the current branch onto the latest default branch, then push that branch to its own remote branch (first push sets the ' +
          'upstream, a later one uses --force-with-lease). Nothing is pushed to the default branch. A rebase that conflicts is ' +
          'undone and answered with the conflicting files. Needs a clean work tree. Refuses on the default branch.',
        inputSchema: { type: 'object', properties: {} },
      })
    } catch (error) {
      $.ui.log(`pr-watch: tools not registered: ${error instanceof Error ? error.message : String(error)}`)
    }
    void refreshGit($)
    void refreshPr($)

    $.clock.every(TICK_MS, () => {
      ticks += 1

      if (ticks % FETCH_TICKS === 0) {
        if (base !== null) void run($, ['git', 'fetch', '--quiet', 'origin', base.replace(/^origin\//, '')], 30_000)
      }

      const isLive = last !== null && (last.overall === 'failed' || last.overall === 'running')

      void refreshGit($)
      if (isLive || ticks % IDLE_TICKS === 0) void refreshPr($)
    })

    return next(e)
  })

  on('command.run', { command: 'pr-watch' }, async $ => {
    await refreshPr($)

    const pr = await read($, snapshot)

    await $.ui.open({ id: PANE, title: pr === null ? 'PR' : `PR #${pr.number}`, rows: pr === null ? 4 : 20 })

    return { text: pr === null ? 'pr-watch: this branch has no pull request.' : summary(pr) }
  })

  on('tool.call', async ($, e, next) => {
    const command = String((e as { command?: unknown }).command ?? '')

    if (/\bgh pr create\b/.test(command)) {
      const now = await read($, gitState)

      if (now !== null && now.behind > 0) {
        $.ui.toast(`${now.behind} behind ${now.base}: this PR goes out unrebased`, { timeoutMs: 8000 })
      }
    }

    const ran = await next(e)

    void refreshGit($)
    if (/\bgh (pr|run) |\bgit (push|checkout|switch)\b/.test(command)) void refreshPr($)

    return ran
  })

  on('turn.start', ($, e, next) => {
    running.add(e.turnId)

    return next(e)
  })

  on('turn.complete', ($, e, next) => {
    running.delete(e.turnId)
    void refreshGit($)
    void refreshPr($)

    return next(e)
  })

  // The branch rides the engine's own hint line, dim, beside its PR pill.
  on('ui.render', { component: 'PromptHint' }, async ($, e, next) => {
    const git = await read($, gitState)

    if (git === null) return next(e)

    const tail = [e.props.tail, `⎇ ${git.branch}`].filter(Boolean).join(' · ')

    return next({ ...e, props: { ...e.props, tail } })
  })

  // Merging is held until the person says yes, whoever asked for it and however green the PR is.
  on('tool.call', { tool: ['Bash', 'PowerShell'] }, async ($, e, next) => {
    const command = String((e as { command?: unknown }).command ?? '')

    if (!MERGE.test(command)) return next(e)

    let answer = 'Do not merge'

    try {
      answer = await $.ui.ask(`Claude is about to merge a pull request. Merge it? ${command.slice(0, 140)}`, ['Merge', 'Do not merge'])
    } catch {
      // Dismissed, or nobody to ask: the answer stays no.
    }

    return answer === 'Merge' ? next(e) : { deny: NO_MERGE }
  }).catch(($, e, next) => (next.called ? next(e) : { deny: NO_MERGE }))

  on('tool.call', { tool: 'mcp__pr-watch__commit' }, async ($, e) =>
    answerOf(await commitTool($, e as { subject?: unknown; body?: unknown; paths?: unknown })),
  )
  on('tool.call', { tool: 'mcp__pr-watch__push' }, async ($, e) => answerOf(await pushTool($, e as { force_with_lease?: unknown })))
  on('tool.call', { tool: 'mcp__pr-watch__rebase_and_push' }, async $ => answerOf(await rebaseTool($)))

  // A key's few words get their instructions here, unseen by the person.
  on('prompt.submit', ($, e, next) => {
    const how = (e.origin as { kind: string }).kind === 'plugin' ? howFor(e.text) : null

    if (how === null) return next(e)

    const before = stopped === null ? [] : [`${TRIED}\n\nWhy it failed:\n${stopped}`]

    stopped = null

    return next({ ...e, context: [...(e.context ?? []), how, ...before] })
  })

  // --- The band above the prompt: PR, uncommitted work, rebase --------------

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.props.hasSurvey) return next(e)

    const pr = await read($, snapshot)
    const git = await read($, gitState)
    const now = pr === null ? null : overall(pr)
    const hasPr =
      pr !== null && git !== null && now !== null && pr.branch === git.branch &&
      now !== 'merged' && now !== 'closed' && (await read($, hiddenPr)) !== keyOf(pr)
    const dirty = git === null ? 0 : git.modified + git.untracked
    // The branch's PR is merged and nothing was committed since: the branch is done.
    const isDone =
      pr !== null && git !== null && pr.state === 'MERGED' && pr.branch === git.branch && pr.headSha === git.head
    const isOpen = pr !== null && git !== null && pr.state === 'OPEN' && pr.branch === git.branch
    // A branch without an upstream has pushed nothing: everything on top of the base is outgoing.
    const outgoing = git === null ? 0 : (git.unpushed ?? git.ahead)
    const isBase = git !== null && `origin/${git.branch}` === git.base
    const hasPush =
      git !== null && dirty === 0 && !isDone && !isBase && (outgoing > 0 || (!isOpen && git.ahead > 0))
    const hasRebase =
      !isDone && git !== null && git.behind > 0 && `origin/${git.branch}` !== git.base && (await read($, hiddenRebase)) !== git.baseSha

    const busy = await read($, doing)
    const halt = await read($, halted)

    if (!hasPr && !isDone && dirty === 0 && !hasPush && !hasRebase && busy === null && halt === null) return next(e)

    const below = await next(e)

    const { Box, Button, Text } = $.ui.resolve(e)
    const width = Math.max(30, e.props.bodyColumns)
    const failed = hasPr ? pr.checks.filter(check => check.state === 'fail') : []
    const skipped = hasPr ? pr.checks.filter(check => check.state === 'skipped').length : 0
    const files = git?.conflicts ?? []

    return (
      <Box flexDirection="column">
        <Box flexDirection="column" width={width}>
        {busy !== null && (
          <Text color="yellow" wrap="truncate-end">● {busy}</Text>
        )}
        {halt !== null && (
          <Box flexDirection="column">
            <Text color="red" wrap="truncate-end">
              ✗ {halt.key} failed · {halt.why.split(/\r?\n/).find(line => line.trim() !== '')?.trim() ?? ''}
            </Text>
            <Box columnGap={2} marginLeft={2}>
              <Button key="resolve" plain hotkey="r" label="Resolve with Claude" onPress={() => resolve($, halt)} />
              <Button key="cancel" plain dimColor hotkey="n" label="Cancel" onPress={() => update($, halted, () => null)} />
            </Box>
          </Box>
        )}
        {hasPr && (
          <Box flexDirection="column">
            <Box columnGap={1}>
              <Text bold color={HEAD[now].color}>
                {HEAD[now].mark} PR #{pr.number}
              </Text>
              <Text color={HEAD[now].color}>{HEAD[now].word}</Text>
              <Text dimColor>·</Text>
              <Box flexGrow={1} flexShrink={1}>
                <Text wrap="truncate-end">
                  {pr.issue === null ? pr.title : `#${pr.issue.number} ${pr.issue.title}`}
                  {pr.issue !== null && pr.issue.total > 0 ? ` · ${pr.issue.done}/${pr.issue.total} done` : ''}
                </Text>
              </Box>
            </Box>
            <Box columnGap={2} flexWrap="wrap" marginLeft={2}>
              {pr.checks
                .filter(check => check.state !== 'skipped')
                .map(check => (
                  <Box>
                    <Text color={COLOR[check.state]}>{GLYPH[check.state]} </Text>
                    <Text bold={check.state === 'fail'} dimColor={check.state === 'pass'}>
                      {check.name.split(' — ')[0]}
                    </Text>
                    <Text dimColor>
                      {check.state === 'queued' ? ' queued' : check.seconds === null ? '' : ` ${duration(check.seconds)}`}
                    </Text>
                  </Box>
                ))}
              {skipped > 0 && <Text dimColor>– {skipped} skipped</Text>}
              {pr.checks.length === 0 && <Text dimColor>no checks reported yet</Text>}
            </Box>
            {failed.map(check => {
              const log = pr.logs.find(one => one.jobId === check.jobId)

              return (
                <Box flexDirection="column" marginLeft={2}>
                  <Text color="red" wrap="truncate-end">
                    ✗ {check.name}
                    {log === undefined ? ' · log arrives when the run finishes' : log.step === '' ? '' : ` › ${log.step}`}
                  </Text>
                  {log !== undefined &&
                    headline(log.lines, 2).map(line => (
                      <Text dimColor wrap="truncate-end">    {line}</Text>
                    ))}
                </Box>
              )
            })}
            <Box columnGap={2} marginLeft={2}>
              {failed.length > 0 && <Button key="retry" plain hotkey="1" label="Retry failed" onPress={() => retry($, pr)} />}
              {failed.length > 0 && <Button key="hand" plain hotkey="2" label="Hand log to Claude" onPress={() => hand($, pr)} />}
              <Button key="web" plain dimColor hotkey="3" label="Open in browser" onPress={() => browse($, pr)} />
              <Button key="hide-pr" plain dimColor hotkey="x" label="Hide" onPress={() => update($, hiddenPr, () => keyOf(pr))} />
            </Box>
          </Box>
        )}
        {git !== null && dirty > 0 && (
          <Box columnGap={1}>
            <Text bold color="cyan">● {dirty} uncommitted</Text>
            <Text dimColor>
              · {[git.modified > 0 ? `${git.modified} modified` : '', git.untracked > 0 ? `${git.untracked} new` : '']
                .filter(Boolean)
                .join(', ')}
              {outgoing > 0 ? ` · ↑ ${outgoing} not pushed` : ''}
            </Text>
            <Text> </Text>
            <Button
              key="commit"
              plain
              hotkey="c"
              label="Commit"
              onPress={() => act($, 'The person pressed Commit', () => commitKey($), ASK.commit)}
            />
          </Box>
        )}
        {hasPush && (
          <Box columnGap={1}>
            <Text bold color="cyan">
              {outgoing > 0 ? `↑ ${plural(outgoing, 'commit')} not pushed` : '↑ pushed'}
            </Text>
            {!isOpen && <Text dimColor>· no PR yet</Text>}
            <Text> </Text>
            <Button
              key="push"
              plain
              hotkey="p"
              label={isOpen ? 'Push' : outgoing > 0 ? 'Push & open PR' : 'Open PR'}
              onPress={() =>
                isOpen
                  ? act($, 'The person pressed Push', () => pushTool($, {}), ASK.push)
                  : act($, 'The person pressed Push & open PR', () => shipKey($), ASK.ship)
              }
            />
          </Box>
        )}
        {isDone && (
          <Box columnGap={1}>
            <Text bold color="magenta">✓ PR #{pr.number} merged</Text>
            <Text dimColor>· this branch is done</Text>
            <Text> </Text>
            {dirty === 0 ? (
              <Button
                key="switch"
                plain
                hotkey="m"
                label={`Switch to ${git.base.replace(/^origin\//, '')}`}
                onPress={() => switchToBase($, git)}
              />
            ) : (
              <Text dimColor>commit or stash first to switch</Text>
            )}
          </Box>
        )}
        {hasRebase && (
          <Box columnGap={1}>
            <Text bold color="yellow">↓ {git.behind} behind {git.base.replace(/^origin\//, '')}</Text>
            <Text dimColor>·</Text>
            {git.conflicts === null && <Text dimColor>conflict check unavailable</Text>}
            {git.conflicts !== null && files.length === 0 && <Text color="green">rebase looks clean</Text>}
            {files.length > 0 && (
              <Text color="red" wrap="truncate-end">
                conflicts likely in {plural(files.length, 'file')}:{' '}
                {files.slice(0, 2).map(path => path.split('/').pop()).join(', ')}
                {files.length > 2 ? ', …' : ''}
              </Text>
            )}
            <Text> </Text>
            <Button
              key="rebase"
              plain
              hotkey="b"
              label="Rebase & push"
              onPress={() => {
                if (dirty === 0 && git.conflicts !== null && files.length === 0) {
                  return act($, 'The person pressed Rebase & push', () => rebaseTool($), rebaseAsk(git.base))
                }
                // Which side wins differs every time: this one is Claude's from the start.
                if (files.length > 0) $.ui.log(`Rebase & push: conflicts likely in ${files.join(', ')}; Claude rebases by hand`)

                return void $.prompt.submit({ text: rebaseAsk(git.base), asUser: true })
              }}
            />
            <Button key="hide-rebase" plain dimColor hotkey="h" label="Hide" onPress={() => update($, hiddenRebase, () => git.baseSha)} />
          </Box>
        )}
        </Box>
        {below}
      </Box>
    )
  })

  // --- The full view, opened with /pr-watch --------------------------------

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e)
    const pr = await read($, snapshot)
    const git = await read($, gitState)
    const halt = await read($, halted)
    // A key that stopped: its reason whole, every line wrapped, none cut.
    const stop = halt !== null && (
      <Box flexDirection="column">
        <Text bold color="red">✗ {halt.key} failed</Text>
        {halt.why.split(/\r?\n/).map(line => (
          <Text>{line === '' ? ' ' : line}</Text>
        ))}
        <Text> </Text>
        <Box columnGap={2}>
          <Button key="resolve" plain hotkey="r" label="Resolve with Claude" onPress={() => resolve($, halt)} />
          <Button key="cancel" plain dimColor hotkey="n" label="Cancel" onPress={() => update($, halted, () => null)} />
        </Box>
        <Text> </Text>
      </Box>
    )

    if (pr === null) {
      return (
        <Box flexDirection="column">
          {stop}
          <Text dimColor>
            {(await read($, isLoaded)) ? 'This branch has no pull request.' : 'Looking for the pull request…'}
          </Text>
        </Box>
      )
    }

    const now = overall(pr)
    const head = HEAD[now]
    const width = Math.max(30, Math.min(e.props.bodyColumns, 76))
    const shown = pr.checks.filter(check => check.state !== 'skipped')
    const skipped = pr.checks.filter(check => check.state === 'skipped')
    const failed = pr.checks.filter(check => check.state === 'fail')
    const room = e.props.placement === 'dock' ? 6 : 3
    const behind = git !== null && git.branch === pr.branch ? git.behind : null

    return (
      <Box flexDirection="column" width={width}>
        {stop}
        <Text bold color={head.color} wrap="truncate-end">
          {head.mark} PR #{pr.number} · {pr.title}
        </Text>
        <Box>
          <Text dimColor>Status  </Text>
          <Text bold color={head.color}>{head.word}</Text>
          {pr.isDraft && <Text dimColor> · draft</Text>}
        </Box>
        {pr.issue !== null && (
          <Box>
            <Text dimColor>Issue   </Text>
            <Text wrap="truncate-end">
              #{pr.issue.number} {pr.issue.title}
              {pr.issue.total > 0 ? ` · ${pr.issue.done}/${pr.issue.total} done` : ''}
            </Text>
          </Box>
        )}
        <Box>
          <Text dimColor>Branch  </Text>
          <Text wrap="truncate-end">{pr.branch}</Text>
          {behind !== null && pr.state === 'OPEN' && (
            <Text color={behind > 0 ? 'yellow' : undefined} dimColor={behind === 0}>
              {behind > 0 ? ` · ${behind} behind ${git?.base.replace(/^origin\//, '') ?? 'base'}` : ' · up to date'}
            </Text>
          )}
        </Box>
        <Text> </Text>
        {pr.checks.length === 0 && <Text dimColor>  No checks reported yet.</Text>}
        {shown.map(check => {
          const log = pr.logs.find(one => one.jobId === check.jobId)

          return (
            <Box flexDirection="column">
              <Box>
                <Text color={COLOR[check.state]}>  {GLYPH[check.state]} </Text>
                <Box flexGrow={1}>
                  <Text bold={check.state === 'fail'} wrap="truncate-end">{check.name}</Text>
                </Box>
                <Text dimColor>
                  {check.state === 'running' ? ' running ' : check.state === 'queued' ? ' queued' : ' '}
                  {duration(check.seconds)}
                </Text>
              </Box>
              {check.state === 'fail' && log === undefined && (
                <Text dimColor italic>      log arrives when the run finishes</Text>
              )}
              {log !== undefined && (
                <Box flexDirection="column" marginLeft={6}>
                  {log.step !== '' && <Text color="red" wrap="truncate-end">{log.step}</Text>}
                  {headline(log.lines, room).map(line => (
                    <Text dimColor wrap="truncate-end">{line}</Text>
                  ))}
                </Box>
              )}
            </Box>
          )
        })}
        {skipped.length > 0 && (
          <Text dimColor italic wrap="truncate-end">
            {'  '}– {skipped.length} skipped by path filter: {skipped.map(check => check.name.split(' — ')[0]).join(', ')}
          </Text>
        )}
        <Text> </Text>
        <Box columnGap={2} flexWrap="wrap">
          {failed.length > 0 && <Button key="retry" plain hotkey="1" label="Retry failed" onPress={() => retry($, pr)} />}
          {failed.length > 0 && <Button key="hand" plain hotkey="2" label="Hand log to Claude" onPress={() => hand($, pr)} />}
          <Button key="web" plain hotkey="3" label="Open in browser" onPress={() => browse($, pr)} />
          <Button key="refresh" plain hotkey="r" label="Refresh" onPress={() => refreshPr($)} />
        </Box>
      </Box>
    )
  })
}
