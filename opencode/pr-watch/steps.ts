import type { PrWatchCheck, PrWatchGit, PrWatchIssue, PrWatchLog, PrWatchSnapshot } from '../../pr-watch/types'
import { countBoxes, issueNumber, parseChecks, parseLog } from '../../pr-watch/hooks/parse'

// The fixed steps of pr-watch, with nothing of a host in them: the server
// plugin runs them as tools the model calls, the TUI plugin runs them for a
// key. Both hand in how a command is run.

export type Ran = { exitCode: number | null; stdout: string; stderr: string }
export type Run = (argv: readonly string[], timeoutMs?: number) => Promise<Ran | null>

/** `isDone` marks a step that went through: a key reads it, the model never sees it. */
export type Answer = { result: string; isDone?: true } | { deny: string }

export const TOOL = {
  commit: 'pr_watch_commit',
  push: 'pr_watch_push',
  rebase: 'pr_watch_rebase_and_push',
} as const

const FIELDS =
  'number,title,body,state,isDraft,headRefOid,headRefName,closingIssuesReferences,statusCheckRollup'
// How much of a diff a model is shown.
const MOST = 200_000

const ENV = { ...process.env, GIT_TERMINAL_PROMPT: '0', GH_PROMPT_DISABLED: '1' }

/** Runs commands in the directory `cwd` names at the time of the call. */
export function runIn(cwd: () => string): Run {
  return async (argv, timeoutMs = 20_000) => {
    try {
      const child = Bun.spawn([...argv], {
        cwd: cwd(),
        env: ENV,
        stdin: 'ignore',
        stdout: 'pipe',
        stderr: 'pipe',
        timeout: timeoutMs,
        windowsHide: true,
      })
      const [stdout, stderr, exitCode] = await Promise.all([
        new Response(child.stdout).text(),
        new Response(child.stderr).text(),
        child.exited,
      ])

      return { exitCode, stdout, stderr }
    } catch {
      return null
    }
  }
}

/** Runs a command that takes its time, handing on the line it last wrote. */
export async function stream(
  cwd: string,
  argv: readonly string[],
  onLine: (line: string) => void,
  timeoutMs: number,
): Promise<Ran | null> {
  try {
    const child = Bun.spawn([...argv], { cwd, env: ENV, stdin: 'ignore', stdout: 'pipe', stderr: 'pipe', timeout: timeoutMs, windowsHide: true })
    const errors = new Response(child.stderr).text()
    const decoder = new TextDecoder()
    let stdout = ''

    for await (const piece of child.stdout) {
      stdout = `${stdout}${decoder.decode(piece, { stream: true }).replace(/\x1b\[[0-9;]*[A-Za-z]/g, '')}`.slice(-20_000)
      onLine((stdout.split(/[\r\n]+/).filter(one => one.trim() !== '').pop() ?? '').trim())
    }

    return { exitCode: await child.exited, stdout, stderr: await errors }
  } catch {
    return null
  }
}

export const why = (ran: { stderr: string; stdout: string } | null): string =>
  ((ran?.stderr || ran?.stdout) ?? 'git did not answer').trim().split('\n').filter(Boolean).pop() ?? ''

export const tail = (ran: { stderr: string; stdout: string } | null, most: number): string =>
  `${ran?.stdout ?? ''}\n${ran?.stderr ?? ''}`.split(/\r?\n/).filter(line => line.trim() !== '').slice(-most).join('\n')

export function createSteps(run: Run, onChange: () => void = () => {}) {
  let base: string | null = null
  let tried: { key: string; conflicts: string[] | null } | null = null
  let issue: (PrWatchIssue & { at: number }) | null = null
  // The remote head the model was shown when a push was refused: a forced push is
  // leased to exactly that commit, so it cannot overwrite anything newer.
  let shown: { branch: string; sha: string } | null = null
  const logs = new Map<string, PrWatchLog>()

  async function text(argv: readonly string[]): Promise<string | null> {
    const ran = await run(argv)

    return ran === null || ran.exitCode !== 0 ? null : ran.stdout.trim()
  }

  // --- The branch: what is uncommitted, unpushed, behind ----------------------

  /** The remote's default branch (`origin/main`), asked once: origin's HEAD, else main, else master. */
  async function findBase(): Promise<string | null> {
    if (base !== null) return base

    const head = await text(['git', 'symbolic-ref', '--short', 'refs/remotes/origin/HEAD'])

    if (head !== null && head !== '') {
      base = head

      return base
    }
    for (const name of ['origin/main', 'origin/master']) {
      if ((await text(['git', 'rev-parse', '--verify', '--quiet', name])) !== null) {
        base = name

        return base
      }
    }

    return null
  }

  async function loadGit(): Promise<PrWatchGit | null> {
    const branch = await text(['git', 'branch', '--show-current'])

    if (branch === null || branch === '') return null

    const onto = await findBase()
    const status = (await run(['git', 'status', '--porcelain']))?.stdout.split(/\r?\n/).filter(Boolean) ?? []
    const untracked = status.filter(line => line.startsWith('??')).length
    const counts =
      (onto === null ? null : await text(['git', 'rev-list', '--left-right', '--count', `HEAD...${onto}`])) ?? '0 0'
    const [ahead = 0, behind = 0] = counts.split(/\s+/).map(Number)
    const unpushed = await text(['git', 'rev-list', '--count', '@{upstream}..HEAD'])
    const baseSha = (onto === null ? null : await text(['git', 'rev-parse', onto])) ?? ''
    const head = (await text(['git', 'rev-parse', 'HEAD'])) ?? ''
    const key = `${head}:${baseSha}`

    if (behind === 0) tried = { key, conflicts: [] }
    if (onto !== null && behind > 0 && tried?.key !== key) {
      // Merges the two tips in memory: nothing in the work tree or the index moves.
      const merged = await run(['git', 'merge-tree', '--write-tree', '--name-only', 'HEAD', onto])
      const names = merged?.stdout.split(/\r?\n\r?\n/)[0]?.split(/\r?\n/).slice(1).filter(Boolean) ?? []

      tried = {
        key,
        conflicts: merged === null || (merged.exitCode ?? 2) > 1 ? null : merged.exitCode === 0 ? [] : names,
      }
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

  /** Brings the default branch's remote tip up to date, so "behind" is true. */
  async function fetchBase() {
    if (base !== null) await run(['git', 'fetch', '--quiet', 'origin', base.replace(/^origin\//, '')], 30_000)
  }

  // --- The pull request: checks, failing log, issue ---------------------------

  async function loadIssue(number: number, now: number) {
    if (issue !== null && issue.number === number && now - issue.at < 300_000) return issue

    const ran = await text(['gh', 'issue', 'view', String(number), '--json', 'title,body'])

    if (ran === null) return issue?.number === number ? issue : null

    const data = JSON.parse(ran) as { title?: string; body?: string }
    issue = { number, title: data.title ?? '', ...countBoxes(data.body ?? ''), at: now }

    return issue
  }

  async function loadLog(check: PrWatchCheck) {
    if (check.jobId === null) return null

    const kept = logs.get(check.jobId)

    if (kept !== undefined) return kept

    const ran = await run(['gh', 'run', 'view', '--job', check.jobId, '--log-failed'], 45_000)

    // gh has no log until the whole run is over: ask again at the next poll.
    if (ran === null || ran.exitCode !== 0 || ran.stdout.trim() === '') return null

    const log = { jobId: check.jobId, ...parseLog(ran.stdout) }
    logs.set(check.jobId, log)

    return log
  }

  async function loadPr(): Promise<PrWatchSnapshot | null> {
    const ran = await text(['gh', 'pr', 'view', '--json', FIELDS])

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
    const now = Date.now()
    const checks = parseChecks(data.statusCheckRollup ?? [], now)
    const found = issueNumber(data.closingIssuesReferences, data.title, data.body ?? '')
    const failed: PrWatchLog[] = []

    for (const check of checks.filter(one => one.state === 'fail')) {
      const log = await loadLog(check)

      if (log !== null) failed.push(log)
    }

    return {
      number: data.number,
      title: data.title,
      state: data.state,
      isDraft: data.isDraft === true,
      headSha: data.headRefOid,
      branch: data.headRefName,
      issue: found === null ? null : await loadIssue(found, now),
      checks,
      logs: failed,
    }
  }

  /** Restarts the failed jobs of a PR; answers with one line per run. */
  async function retry(pr: PrWatchSnapshot): Promise<string[]> {
    const failed = pr.checks.filter(check => check.state === 'fail')
    const runs = [...new Set(failed.map(check => check.runId).filter((id): id is string => id !== null))]
    const said: string[] = []

    for (const id of runs) {
      const ran = await run(['gh', 'run', 'rerun', id, '--failed'], 30_000)

      said.push(
        ran?.exitCode === 0
          ? `PR #${pr.number}: failed jobs restarted`
          : `Retry refused: ${(ran?.stderr ?? 'gh did not answer').trim().split('\n')[0]}`,
      )
    }
    for (const check of failed) if (check.jobId !== null) logs.delete(check.jobId)

    return said
  }

  // --- The steps: fixed, with an answer the model reads -------------------------

  /** The default branch is never committed to, pushed or rebased by a step. */
  async function branchFor(): Promise<PrWatchGit | { deny: string }> {
    const git = await loadGit()

    if (git === null) return { deny: 'This directory is not on a git branch.' }
    if (`origin/${git.branch}` === git.base) {
      return { deny: `${git.branch} is the default branch. Create a branch first (git switch -c <type>/<name>), then call the tool again.` }
    }

    return git
  }

  async function commit(input: { subject?: unknown; body?: unknown; paths?: unknown }): Promise<Answer> {
    const git = await branchFor()

    if ('deny' in git) return git

    const subject = typeof input.subject === 'string' ? input.subject.trim() : ''
    const body = typeof input.body === 'string' ? input.body.trim() : ''
    const paths = Array.isArray(input.paths) ? input.paths.filter((path): path is string => typeof path === 'string') : []

    if (subject === '') return { deny: 'A commit needs a subject.' }

    const added = await run(paths.length > 0 ? ['git', 'add', '--', ...paths] : ['git', 'add', '-A'], 60_000)

    if (added?.exitCode !== 0) return { result: `git add failed: ${why(added)}\nNothing was committed.` }

    const staged = await run(['git', 'diff', '--cached', '--quiet'])

    if (staged?.exitCode === 0) return { result: 'There is nothing to commit for these paths.' }

    // A repo's commit hooks run here and may take their time.
    const committed = await run(['git', 'commit', '-m', subject, ...(body === '' ? [] : ['-m', body])], 300_000)

    if (committed?.exitCode !== 0) {
      return { result: `git refused the commit.\nThe changes stay staged. Output:\n${tail(committed, 30)}` }
    }

    const sha = (await text(['git', 'rev-parse', '--short', 'HEAD'])) ?? ''
    const left = (await run(['git', 'status', '--porcelain']))?.stdout.split(/\r?\n/).filter(Boolean).length ?? 0

    onChange()

    return {
      result: `Committed ${sha} ${subject}. ${left === 0 ? 'Nothing is left uncommitted.' : `${left} files are still uncommitted.`} Not pushed.`,
      isDone: true,
    }
  }

  async function push(input: { force_with_lease?: unknown }): Promise<Answer> {
    const git = await branchFor()

    if ('deny' in git) return git

    const isForced = input.force_with_lease === true
    const upstream = await text(['git', 'rev-parse', '--abbrev-ref', '@{upstream}'])
    const lease = shown !== null && shown.branch === git.branch ? `--force-with-lease=${git.branch}:${shown.sha}` : '--force-with-lease'
    const argv = isForced
      ? ['git', 'push', lease, 'origin', git.branch]
      : upstream === null
        ? ['git', 'push', '--set-upstream', 'origin', git.branch]
        : ['git', 'push']
    const pushed = await run(argv, 180_000)

    if (pushed?.exitCode === 0) {
      shown = null
      onChange()

      return { result: `Pushed ${git.branch} to origin${isForced ? ' with --force-with-lease' : ''}.`, isDone: true }
    }
    if (isForced || !/rejected|non-fast-forward|fetch first|stale info/.test(`${pushed?.stderr}`)) {
      return { result: `The push failed: ${why(pushed)}` }
    }

    // Refused: show what the remote holds that the local branch lacks.
    await run(['git', 'fetch', '--quiet', 'origin', git.branch], 30_000)

    const sha = (await text(['git', 'rev-parse', `origin/${git.branch}`])) ?? ''
    const theirs = (await text(['git', 'log', '--format=%h %an: %s', '-10', `HEAD..origin/${git.branch}`])) ?? ''

    shown = { branch: git.branch, sha }

    return {
      result:
        `The remote branch has commits this one lacks.\norigin/${git.branch} holds:\n${theirs}\n` +
        `If these are this branch's own commits from before a rebase, call the tool again with force_with_lease: true. ` +
        `If any is someone else's work, do not force: stop and tell the person.`,
    }
  }

  async function rebase(): Promise<Answer> {
    const git = await branchFor()

    if ('deny' in git) return git
    if (git.modified + git.untracked > 0) {
      return { deny: `There are uncommitted changes. Commit them first (${TOOL.commit}), then call this tool again.` }
    }

    const onto = git.base.replace(/^origin\//, '')

    await run(['git', 'fetch', '--quiet', 'origin', onto], 30_000)

    const behind = await text(['git', 'rev-list', '--count', `HEAD..${git.base}`])
    const isRebased = behind !== '0'
    const rebased = isRebased ? await run(['git', 'rebase', git.base], 180_000) : null

    if (isRebased && rebased?.exitCode !== 0) {
      const files = (await text(['git', 'diff', '--name-only', '--diff-filter=U'])) ?? ''

      // Back to exactly where the branch was: nothing half-done is left behind.
      await run(['git', 'rebase', '--abort'])
      // Merging the two tips can be clean where replaying commit by commit is not: what the rebase hit is what is known now.
      tried = { key: `${git.head}:${(await text(['git', 'rev-parse', git.base])) ?? ''}`, conflicts: files.split(/\r?\n/).filter(Boolean) }
      onChange()

      return {
        result:
          `The rebase onto ${git.base} has conflicts.\nIt was undone and the branch is as before.\n` +
          (files === '' ? `git said: ${why(rebased)}` : `Conflicts in:\n${files}`),
      }
    }

    // The branch itself is pushed, to its own remote branch: a first push sets the
    // upstream, a later one is leased, so it refuses when the remote branch moved
    // since it was last seen.
    const upstream = await text(['git', 'rev-parse', '--abbrev-ref', '@{upstream}'])
    const pushed = await run(
      upstream === null ? ['git', 'push', '--set-upstream', 'origin', git.branch] : ['git', 'push', '--force-with-lease'],
      180_000,
    )
    const did = isRebased ? `Rebased ${git.branch} onto ${git.base}` : `${git.branch} was already on top of ${git.base}`
    const outcome =
      pushed?.exitCode === 0
        ? `${did} and pushed it to origin/${git.branch}${isRebased && upstream !== null ? ' with --force-with-lease; its commits have new ids' : ''}. A PR can be opened from it now.`
        : `The push was refused: ${why(pushed)}\n${did}; only the push is missing.`

    onChange()

    return pushed?.exitCode === 0 ? { result: outcome, isDone: true } : { result: outcome }
  }

  /**
   * The uncommitted work as text: every changed file by name and size of change,
   * then the diff, new files whole. A long diff is cut and says so: the list of
   * files stays complete, and the fork knows from the session what was done.
   */
  async function changes(): Promise<{ shown: string; files: number }> {
    const status = ((await text(['git', 'status', '--porcelain'])) ?? '').split(/\r?\n/).filter(Boolean)
    const log = (await text(['git', 'log', '-8', '--format=%s'])) ?? ''
    const stat = (await text(['git', 'diff', 'HEAD', '--stat'])) ?? ''
    const diff = (await run(['git', 'diff', 'HEAD']))?.stdout ?? ''
    const fresh = ((await text(['git', 'ls-files', '--others', '--exclude-standard'])) ?? '').split(/\r?\n/).filter(Boolean)
    let all = diff

    for (const path of fresh) {
      if (all.length > MOST) break

      all = `${all}\n${(await run(['git', 'diff', '--no-index', '--', '/dev/null', path]))?.stdout ?? ''}`
    }

    return {
      files: status.length,
      shown:
        `Last subjects:\n${log}\n\ngit status --porcelain:\n${status.join('\n')}\n\nNew files:\n${fresh.join('\n')}\n\n` +
        `git diff --stat:\n${stat}\n\nDiff:\n${all.slice(0, MOST)}` +
        (all.length > MOST ? '\n(The diff is cut here. The lists above name every changed file.)' : ''),
    }
  }

  return { text, loadGit, loadPr, fetchBase, retry, branchFor, commit, push, rebase, changes }
}

export type Steps = ReturnType<typeof createSteps>
