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

const COMMIT =
  'Commit all uncommitted changes and push this branch. Before you write the message, read what this repo ' +
  'asks of a commit (AGENTS.md, CLAUDE.md, the subjects in `git log`, the issue this branch works on) and ' +
  'follow it. If the changes are clearly several unrelated things, make one commit each. On the default branch, ' +
  'create a branch first. Do not open a PR.'
const PUSH = 'Push this branch to its remote. Tell me first if that would need a force-push.'
const rebaseAsk = (base: string): string =>
  `Rebase this branch onto the latest ${base} (fetch first) and resolve conflicts if there are any. ` +
  'Do not push; tell me if the branch needs a force-push afterwards.'

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
  await refreshGit($)
  await refreshPr($)
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'pr-watch',
      description: "Open the full view of this branch's PR: every check, failing log lines, issue",
    })
    // An earlier version pinned the branch here; a pin outlives a reload until cleared.
    $.ui.status(undefined)
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
    const hasPush = git !== null && dirty === 0 && (git.unpushed ?? 0) > 0
    // The branch's PR is merged and nothing was committed since: the branch is done.
    const isDone =
      pr !== null && git !== null && pr.state === 'MERGED' && pr.branch === git.branch && pr.headSha === git.head
    const hasRebase =
      !isDone && git !== null && git.behind > 0 && `origin/${git.branch}` !== git.base && (await read($, hiddenRebase)) !== git.baseSha

    if (!hasPr && !isDone && dirty === 0 && !hasPush && !hasRebase) return next(e)

    const { Box, Button, Text } = $.ui.resolve(e)
    const width = Math.max(30, e.props.bodyColumns)
    const failed = hasPr ? pr.checks.filter(check => check.state === 'fail') : []
    const skipped = hasPr ? pr.checks.filter(check => check.state === 'skipped').length : 0
    const files = git?.conflicts ?? []

    return (
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
              {(git.unpushed ?? 0) > 0 ? ` · ↑ ${git.unpushed} unpushed` : ''}
            </Text>
            <Text> </Text>
            <Button
              key="commit"
              plain
              hotkey="c"
              label="Commit & push"
              onPress={async () => {
                await $.prompt.submit({ text: COMMIT, asUser: true })
              }}
            />
          </Box>
        )}
        {hasPush && (
          <Box columnGap={1}>
            <Text bold color="cyan">↑ {plural(git.unpushed ?? 0, 'commit')} unpushed</Text>
            <Text> </Text>
            <Button
              key="push"
              plain
              hotkey="p"
              label="Push"
              onPress={async () => {
                await $.prompt.submit({ text: PUSH, asUser: true })
              }}
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
              label="Rebase now"
              onPress={async () => {
                await $.prompt.submit({ text: rebaseAsk(git.base), asUser: true })
              }}
            />
            <Button key="hide-rebase" plain dimColor hotkey="h" label="Hide" onPress={() => update($, hiddenRebase, () => git.baseSha)} />
          </Box>
        )}
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
