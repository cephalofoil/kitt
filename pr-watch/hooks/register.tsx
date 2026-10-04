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

// The tools Claude calls to act on the branch. A key of the band never runs git
// itself: it asks Claude in a few words, Claude decides whether and how to call
// the tool, the tool does the fixed steps and answers with what happened.
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
  rebase: 'Rebase and push this branch onto ',
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
    `Sent by the Rebase key of the pr-watch band. To the person, Rebase means rebased and pushed. Call the tool ${TOOL.rebase}. ` +
    `If it answers that the rebase conflicts, rebase by hand: resolve each conflict only where the right result is clear from both ` +
    `sides, and where it is not, stop with the rebase still open and ask, naming the file and the two versions. After a rebase by ` +
    `hand, call ${TOOL.push} with force_with_lease. Answer in one or two lines.`,
} as const

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

      if (pr !== null && pr.state === 'OPEN' && pr.branch === now.branch && autoFor !== key) {
        autoFor = key
        $.ui.toast(`${now.behind} behind ${now.base.replace(/^origin\//, '')}: rebasing (auto-rebase is on)`, { timeoutMs: 8000 })
        void $.prompt.submit({ text: `${ASK.rebase}${now.base}.` })
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

type Answer = { result: string } | { deny: string }

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

  if (added?.exitCode !== 0) return { result: `Nothing was committed: git add failed: ${why(added)}` }

  const staged = await run($, ['git', 'diff', '--cached', '--quiet'])

  if (staged?.exitCode === 0) return { result: 'Nothing was committed: there is nothing staged for these paths.' }

  // A repo's commit hooks run here and may take their time.
  const committed = await run($, ['git', 'commit', '-m', subject, ...(body === '' ? [] : ['-m', body])], 300_000)

  if (committed?.exitCode !== 0) {
    return { result: `The commit was refused; the changes stay staged. Output:\n${tail(committed, 30)}` }
  }

  const sha = (await text($, ['git', 'rev-parse', '--short', 'HEAD'])) ?? ''
  const left = (await run($, ['git', 'status', '--porcelain']))?.stdout.split(/\r?\n/).filter(Boolean).length ?? 0

  await refreshGit($)

  return { result: `Committed ${sha} ${subject}. ${left === 0 ? 'Nothing is left uncommitted.' : `${left} files are still uncommitted.`} Not pushed.` }
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

    return { result: `Pushed ${git.branch} to origin${isForced ? ' with --force-with-lease' : ''}.` }
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
      `The push was refused: origin/${git.branch} holds commits the local branch does not have:\n${theirs}\n` +
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

  if (behind === '0') return { result: `${git.branch} is already on top of ${git.base}. Nothing was rebased or pushed.` }

  const rebased = await run($, ['git', 'rebase', git.base], 180_000)

  if (rebased?.exitCode !== 0) {
    const files = (await text($, ['git', 'diff', '--name-only', '--diff-filter=U'])) ?? ''

    // Back to exactly where the branch was: nothing half-done is left behind.
    await run($, ['git', 'rebase', '--abort'])
    await refreshGit($)

    return {
      result:
        `A plain rebase onto ${git.base} does not go through; it was undone and the branch is as before.\n` +
        (files === '' ? `git said: ${why(rebased)}` : `Conflicts in:\n${files}`),
    }
  }

  const upstream = await text($, ['git', 'rev-parse', '--abbrev-ref', '@{upstream}'])
  let outcome = `Rebased ${git.branch} onto ${git.base}. The branch has no upstream, so nothing was pushed.`

  if (upstream !== null) {
    // The lease refuses when the remote branch moved since it was last seen.
    const pushed = await run($, ['git', 'push', '--force-with-lease'], 180_000)

    outcome =
      pushed?.exitCode === 0
        ? `Rebased ${git.branch} onto ${git.base} and pushed with --force-with-lease. Its commits have new ids.`
        : `Rebased ${git.branch} onto ${git.base}, but the push was refused: ${why(pushed)}`
  }
  await refreshGit($)
  await refreshPr($)

  return { result: outcome }
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
          'Rebase the current branch onto the latest default branch and push it with --force-with-lease. A rebase that conflicts is ' +
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

  on('turn.complete', ($, e, next) => {
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
  }).catch(() => ({ deny: NO_MERGE }))

  on('tool.call', { tool: 'mcp__pr-watch__commit' }, ($, e) => commitTool($, e as { subject?: unknown; body?: unknown; paths?: unknown }))
  on('tool.call', { tool: 'mcp__pr-watch__push' }, ($, e) => pushTool($, e as { force_with_lease?: unknown }))
  on('tool.call', { tool: 'mcp__pr-watch__rebase_and_push' }, $ => rebaseTool($))

  // A key's few words get their instructions here, unseen by the person.
  on('prompt.submit', ($, e, next) => {
    const how = (e.origin as { kind: string }).kind === 'plugin' ? howFor(e.text) : null

    return how === null ? next(e) : next({ ...e, context: [...(e.context ?? []), how] })
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

    if (!hasPr && !isDone && dirty === 0 && !hasPush && !hasRebase) return next(e)

    const below = await next(e)

    const { Box, Button, Text } = $.ui.resolve(e)
    const width = Math.max(30, e.props.bodyColumns)
    const failed = hasPr ? pr.checks.filter(check => check.state === 'fail') : []
    const skipped = hasPr ? pr.checks.filter(check => check.state === 'skipped').length : 0
    const files = git?.conflicts ?? []

    return (
      <Box flexDirection="column">
        <Box flexDirection="column" width={width}>
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
              onPress={() => void $.prompt.submit({ text: ASK.commit, asUser: true })}
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
              onPress={() => void $.prompt.submit({ text: isOpen ? ASK.push : ASK.ship, asUser: true })}
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
              onPress={() => void $.prompt.submit({ text: `${ASK.rebase}${git.base}.`, asUser: true })}
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

    if (pr === null) {
      return (
        <Box flexDirection="column">
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
