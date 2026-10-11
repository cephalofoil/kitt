/** @jsxImportSource @opentui/solid */
import { join } from 'node:path'

import type { TuiPlugin, TuiPluginModule } from '@opencode-ai/plugin/tui'
import { createMemo, createSignal, For, type JSX, Show } from 'solid-js'

import { duration, headline, overall } from '../../pr-watch/hooks/parse'
import type { PrWatchGit, PrWatchSnapshot } from '../../pr-watch/types'
import { type Answer, createSteps, runIn, stream, tail, TOOL, why } from './steps'

// The TUI half of pr-watch: the band above the prompt, its keys, and the full
// view. A key runs its fixed steps itself, without a turn; the one thing it
// needs a model for, a commit message or a PR's text, it asks of a fork of the
// session. When a step does not go through, the band says so in one line with
// two keys: resolve it with the agent, or cancel. Only the first starts a turn.

const TICK_MS = 15_000
const IDLE_TICKS = 4
const FETCH_TICKS = 20
// How many changed files a commit by key may have.
const FILES = 1000

// What a key sends is a few words the person reads in the transcript. How to do
// it rides along as a part only the model reads.
const ASK = {
  commit: 'Commit my changes.',
  ship: 'Push this branch and open a PR.',
  push: 'Push this branch.',
  rebase: 'Rebase this branch onto ',
} as const

const HOW = {
  commit:
    `Sent by the Commit key of the pr-watch band. Look at what is uncommitted, write the message this repo asks for (AGENTS.md, ` +
    `the subjects in \`git log\`, the issue the branch works on), and call the tool ${TOOL.commit} with it. For ` +
    `clearly unrelated changes, call it once per group with \`paths\`. Do not run \`git commit\` yourself, do not push, do not open a PR. ` +
    `If something should not be committed (a secret, generated junk, work that is visibly broken), do not call the tool: say what and why. ` +
    `Answer with the commit subject and nothing else.`,
  ship:
    `Sent by the Push & open PR key of the pr-watch band. First run the repo's own checks for what changed: \`kitt check\` when the ` +
    `repo has a kitt.toml, otherwise the checks AGENTS.md names. If a check fails, stop and report it. If the branch is ` +
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

// What a key asks of a fork of the session: text alone, or "ASK:" to hand the key to the agent.
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

// What the agent reads beside a key's few words when the key tried first and the person had it resolved.
const TRIED =
  `The key ran its fixed steps itself first, without a turn; a step failed, and the person chose to have you do it. Why it failed ` +
  `follows. Say in one line what went wrong, then do what the key was doing, leaving out what already went through. Ask first only ` +
  `where the decision is the person's: a secret or junk among the changes, someone else's commits on the remote. Keep the answer ` +
  `to a few lines.`

// The band's keys. opencode has no focus on a band, so a key is a chord after
// the leader; every one is also in the command palette and can be clicked.
const KEYS = {
  'prwatch.open': '<leader>w',
  'prwatch.commit': '<leader>i',
  'prwatch.push': '<leader>p',
  'prwatch.rebase': '<leader>o',
  'prwatch.resolve': '<leader>f',
  'prwatch.cancel': '<leader>d',
} as const

const GLYPH = { pass: '✓', fail: '✗', running: '●', queued: '○', skipped: '–' } as const
const HEAD = {
  failed: { color: 'error', mark: '✗', word: 'checks failing' },
  running: { color: 'warning', mark: '●', word: 'checks running' },
  green: { color: 'success', mark: '✓', word: 'all checks green' },
  merged: { color: 'secondary', mark: '✓', word: 'merged' },
  closed: { color: 'textMuted', mark: '–', word: 'closed' },
} as const

type Halt = { key: string; why: string; text: string; how: string }

const keyOf = (pr: PrWatchSnapshot): string => `${pr.number}:${pr.headSha}`
const plural = (count: number, one: string): string => `${count} ${one}${count === 1 ? '' : 's'}`
const short = (base: string): string => base.replace(/^origin\//, '')

/** What the Rebase key sends: the branch goes onto the base, then the branch itself is pushed. */
const rebaseAsk = (base: string): string => `${ASK.rebase}${short(base)} and push the branch.`

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

const tui: TuiPlugin = async (api, options) => {
  const isAutoRebase = options?.autoRebase === true
  const keys: Record<string, string> = { ...KEYS, ...(options?.keybinds as Record<string, string> | undefined) }
  const cwd = () => api.state.path.directory || process.cwd()
  const run = runIn(cwd)
  const theme = () => api.theme.current

  const [pr, setPr] = createSignal<PrWatchSnapshot | null>(null)
  const [git, setGit] = createSignal<PrWatchGit | null>(null)
  const [isLoaded, setLoaded] = createSignal(false)
  const [hiddenPr, setHiddenPr] = createSignal<string | null>(null)
  const [hiddenRebase, setHiddenRebase] = createSignal<string | null>(null)
  // What a key is doing right now, as the band shows it; null when none is.
  const [doing, setDoing] = createSignal<string | null>(null)
  // The key whose steps failed, why, and what the agent is sent when the person has it resolved.
  const [halted, setHalted] = createSignal<Halt | null>(null)

  let isPrBusy = false
  let isGitBusy = false
  let isGitStale = false
  let isActing = false
  let ticks = 0
  let autoFor = ''
  let last: { key: string; overall: ReturnType<typeof overall> } | null = null

  const toast = (message: string, variant: 'info' | 'success' | 'warning' | 'error' = 'info') =>
    api.ui.toast({ message, variant, duration: 8000 })

  /** The session on screen; a key acts for it. */
  const sessionID = (): string | null => {
    const route = api.route.current

    return route.name === 'session' ? ((route.params as { sessionID?: string } | undefined)?.sessionID ?? null) : null
  }

  // git is not moved under a turn that is working.
  const isWorking = (id: string): boolean => {
    const status = api.state.session.status(id)

    return status !== undefined && status.type !== 'idle'
  }

  const steps = createSteps(run, () => {
    void refreshGit()
    void refreshPr()
  })

  // --- What is known: the branch, the pull request ---------------------------

  async function refreshGit() {
    if (isGitBusy) {
      isGitStale = true

      return
    }

    isGitBusy = true

    try {
      const now = await steps.loadGit()

      setGit(now)

      if (isAutoRebase && now !== null && now.behind > 0 && now.modified + now.untracked === 0 && now.conflicts?.length === 0) {
        const open = pr()
        const key = `${now.head}:${now.baseSha}`
        const id = sessionID()

        if (open !== null && open.state === 'OPEN' && open.branch === now.branch && autoFor !== key && !isActing && (id === null || !isWorking(id))) {
          autoFor = key
          toast(`${now.behind} behind ${short(now.base)}: rebasing (auto-rebase is on)`)
          void act('Auto-rebase ran', () => steps.rebase(), rebaseAsk(now.base), HOW.rebase)
        }
      }
    } finally {
      isGitBusy = false
      if (isGitStale) {
        isGitStale = false
        void refreshGit()
      }
    }
  }

  function summary(now: PrWatchSnapshot): string {
    const state = overall(now)
    const bad = now.checks.filter(check => check.state === 'fail').map(check => check.name)

    return state === 'failed'
      ? `PR #${now.number} ✗ ${bad[0]}${bad.length > 1 ? ` +${bad.length - 1}` : ''}`
      : `PR #${now.number} ${HEAD[state].mark} ${HEAD[state].word}`
  }

  async function refreshPr() {
    if (isPrBusy) return

    isPrBusy = true

    try {
      const now = await steps.loadPr()
      const state = now === null ? null : overall(now)
      const before = now !== null && last !== null && last.key === keyOf(now) ? last.overall : null

      setPr(now)
      setLoaded(true)
      last = now === null || state === null ? null : { key: keyOf(now), overall: state }

      if (now !== null && before !== null && before !== state && state !== 'running' && state !== 'closed') {
        toast(summary(now), state === 'failed' ? 'error' : 'success')
      }
    } catch {
      // gh answered with something that is no PR: the band stays as it was.
    } finally {
      isPrBusy = false
    }
  }

  // --- What the agent is told and asked ---------------------------------------

  /** Tells the agent what a key did to the branch, without starting a turn. */
  async function tell(what: string) {
    const id = sessionID()

    if (id === null) return

    try {
      await api.client.session.prompt({ sessionID: id, noReply: true, parts: [{ type: 'text', text: `[pr-watch] ${what}` }] })
    } catch {
      // A session that takes no notes still has the toast.
    }
  }

  /** Starts a turn with a key's few words; `unseen` rides along where only the model reads it. */
  async function send(text: string, ...unseen: string[]) {
    const id = sessionID()

    if (id === null) return toast('Open a session first: this key hands the work to the agent.', 'warning')

    await api.client.session.promptAsync({
      sessionID: id,
      parts: [{ type: 'text', text }, ...unseen.map(one => ({ type: 'text' as const, text: one, synthetic: true }))],
    })
  }

  /**
   * Asks a fork of the session for text: its first line and the rest, or why it
   * was not written. The fork has every tool denied and is deleted afterwards.
   */
  async function write(what: string, prompt: string): Promise<{ head: string; rest: string } | { result: string }> {
    const id = sessionID()

    if (id === null) return { result: `The ${what} needs a session to write it.` }

    let fork: string | undefined

    try {
      fork = (await api.client.session.fork({ sessionID: id })).data?.id

      if (fork === undefined) return { result: `The ${what} could not be written (the session could not be forked).` }

      const reply = await api.client.session.prompt({ sessionID: fork, tools: { '*': false }, parts: [{ type: 'text', text: prompt }] })
      const said = (reply.data?.parts ?? [])
        .map(part => (part.type === 'text' ? part.text : ''))
        .join('')
        .trim()
        .replace(/^```\w*\r?\n|\r?\n```$/g, '')
        .trim()
      const [head = '', ...rest] = said.split(/\r?\n/)

      if (head.startsWith('ASK:')) return { result: said.slice(4).trim() }
      if (head === '' || head.length > 200) return { result: `The ${what} came back without a one-line subject.` }

      return { head: head.trim(), rest: rest.join('\n').trim() }
    } catch (error) {
      return { result: `The ${what} could not be written (${error instanceof Error ? error.message : String(error)}).` }
    } finally {
      if (fork !== undefined) void api.client.session.delete({ sessionID: fork }).catch(() => {})
    }
  }

  // --- The keys: the same steps as the tools, without a turn ---------------------

  /** The person chose the agent: the key's own few words are sent, the reason rides along unseen. */
  async function resolve() {
    const halt = halted()

    if (halt === null) return

    setHalted(null)
    await send(halt.text, halt.how, `${TRIED}\n\nWhy it failed:\n${halt.why}`)
  }

  /**
   * Runs a key's fixed steps, without a turn, and tells the agent what was done.
   * `text` and `how` are what the key asks of the agent when a step fails and
   * the person has it resolved.
   */
  async function act(said: string, step: () => Promise<Answer>, text: string, how: string) {
    if (isActing) return

    const key = said.replace(/^The person pressed | ran$/g, '')
    const id = sessionID()

    if (id !== null && isWorking(id)) {
      return toast(`${key} waits: the agent is working. Press it again when the turn has ended.`, 'warning')
    }

    isActing = true
    setHalted(null)
    setDoing(key)

    try {
      const out = await step()

      if ('deny' in out) {
        toast(out.deny, 'warning')
      } else if (out.isDone === true) {
        toast(out.result, 'success')
        await tell(`${said}: ${out.result}`)
      } else {
        setHalted({ key, why: out.result, text, how })
      }
    } catch (error) {
      // A step that threw is a step that did not go through.
      setHalted({ key, why: error instanceof Error ? error.message : String(error), text, how })
    } finally {
      isActing = false
      setDoing(null)
    }
  }

  async function commitKey(): Promise<Answer> {
    const now = await steps.branchFor()

    if ('deny' in now) return { result: now.deny.replace('. ', '.\n') }

    const work = await steps.changes()

    // Thousands of files are not a change someone made by hand: a folder that wants ignoring, more likely.
    if (work.files > FILES) {
      return { result: `${work.files} files are uncommitted: too many for one commit.\nThat looks like generated files or a folder that should be ignored. Nothing was committed.` }
    }

    const message = await write('commit message', `${WRITE.commit}\n\n${work.shown}`)

    return 'result' in message ? message : steps.commit({ subject: message.head, body: message.rest })
  }

  async function shipKey(): Promise<Answer> {
    const now = await steps.branchFor()

    if ('deny' in now) return { result: now.deny.replace('. ', '.\n') }

    // A repo without a kitt.toml names no checks a key could run: the PR is opened all the same, and says so.
    const hasKitt = await Bun.file(join(cwd(), 'kitt.toml')).exists()
    const checked = hasKitt
      ? await stream(cwd(), ['kitt', 'check'], line => setDoing(`kitt check · ${line.slice(0, 90)}`), 600_000)
      : { exitCode: 0, stdout: '', stderr: '' }
    const checks = hasKitt ? 'kitt check passed' : 'no checks were run (this repo has no kitt.toml)'

    if (checked === null) return { result: '`kitt check` could not be started.\nNothing was pushed.' }
    if (checked.exitCode !== 0) return { result: `\`kitt check\` failed.\nNothing was pushed. Output:\n${tail(checked, 40)}` }
    if (now.behind > 0 && now.conflicts?.length !== 0) {
      return { result: `The branch is ${now.behind} behind ${now.base} and the rebase has conflicts.\nNothing was pushed (${checks}).` }
    }

    setDoing(now.behind > 0 ? 'rebasing and pushing' : 'pushing')

    const pushed = now.behind > 0 ? await steps.rebase() : await steps.push({})

    if ('deny' in pushed) return { result: pushed.deny }
    if (pushed.isDone !== true) return { result: pushed.result }

    const onto = short(now.base)
    const commits = (await steps.text(['git', 'log', '--format=%h %s%n%b', `${now.base}..HEAD`])) ?? ''
    const stat = (await steps.text(['git', 'diff', '--stat', `${now.base}...HEAD`])) ?? ''
    const proof = await steps.text(['kitt', 'proof', 'status'])

    setDoing('pushed · writing the PR')

    const text = await write(
      'PR text',
      `${WRITE.pr}\n\nBranch ${now.branch} onto ${onto}.\n\nChecks: ${checks}. Say of the checks only this.\n\nCommits:\n${commits.slice(0, 200_000)}\n\nFiles:\n${stat}` +
        (proof === null || proof === '' ? '' : `\n\nkitt proof status:\n${proof}`),
    )

    if ('result' in text) return { result: `${text.result}\nThe branch is pushed (${checks}); no PR was opened.` }

    const opened = await run(['gh', 'pr', 'create', '--base', onto, '--head', now.branch, '--title', text.head, '--body', text.rest], 60_000)

    await refreshPr()

    return opened?.exitCode === 0
      ? { result: `Pushed and PR opened, ${checks}: ${opened.stdout.trim().split(/\r?\n/).pop() ?? ''}`, isDone: true }
      : { result: `The PR could not be opened: ${ghSaid(opened)}\nThe branch is pushed (${checks}). gh said: ${why(opened)}` }
  }

  async function retry() {
    const now = pr()

    if (now === null) return

    for (const line of await steps.retry(now)) toast(line)

    await refreshPr()
  }

  async function hand() {
    const now = pr()

    if (now === null) return

    const parts = now.checks
      .filter(check => check.state === 'fail')
      .map(check => {
        const log = now.logs.find(one => one.jobId === check.jobId)

        return log === undefined
          ? `Job "${check.name}" failed; its log is not available yet.`
          : `Job "${check.name}" failed in step "${log.step}". Log tail:\n\`\`\`\n${log.lines.join('\n')}\n\`\`\``
      })

    await send(`CI on PR #${now.number} (${now.branch}) is failing.\n\n${parts.join('\n\n')}\n\nFind the cause and fix it.`)
  }

  const browse = () => {
    const now = pr()

    if (now !== null) void run(['gh', 'pr', 'view', String(now.number), '--web'])
  }

  /** Leaves a merged branch for the default one, brought up to the remote's tip. */
  async function switchToBase() {
    const now = git()

    if (now === null) return

    const name = short(now.base)
    const first = (ran: { stderr: string } | null) => (ran?.stderr ?? 'git did not answer').trim().split('\n')[0]

    await run(['git', 'fetch', '--quiet', 'origin', name], 30_000)

    const moved = await run(['git', 'switch', name])

    if (moved?.exitCode !== 0) return toast(`Switch refused: ${first(moved)}`, 'error')

    const pulled = await run(['git', 'merge', '--ff-only', now.base])

    toast(pulled?.exitCode === 0 ? `On ${name}, level with ${now.base}` : `On ${name}, not fast-forwarded: ${first(pulled)}`)
    await tell(`The person pressed Switch: this checkout is now on ${name}, level with ${now.base}. The branch ${now.branch} is merged and done.`)
    await refreshGit()
    await refreshPr()
  }

  // --- What the band shows, worked out once for the band and the commands ------

  const view = createMemo(() => {
    const open = pr()
    const now = git()
    const state = open === null ? null : overall(open)
    const isSame = open !== null && now !== null && open.branch === now.branch
    const dirty = now === null ? 0 : now.modified + now.untracked
    // The branch's PR is merged and nothing was committed since: the branch is done.
    const isDone = isSame && open.state === 'MERGED' && open.headSha === now.head
    const isOpen = isSame && open.state === 'OPEN'
    // A branch without an upstream has pushed nothing: everything on top of the base is outgoing.
    const outgoing = now === null ? 0 : (now.unpushed ?? now.ahead)
    const isBase = now !== null && `origin/${now.branch}` === now.base

    return {
      state,
      dirty,
      isDone,
      isOpen,
      outgoing,
      hasPr: isSame && state !== 'merged' && state !== 'closed' && hiddenPr() !== keyOf(open),
      hasPush: now !== null && dirty === 0 && !isDone && !isBase && (outgoing > 0 || (!isOpen && now.ahead > 0)),
      hasRebase: !isDone && now !== null && now.behind > 0 && !isBase && hiddenRebase() !== now.baseSha,
      failed: open === null ? [] : open.checks.filter(check => check.state === 'fail'),
    }
  })

  const commit = () => act('The person pressed Commit', commitKey, ASK.commit, HOW.commit)

  const push = () =>
    view().isOpen
      ? act('The person pressed Push', () => steps.push({}), ASK.push, HOW.push)
      : act('The person pressed Push & open PR', shipKey, ASK.ship, HOW.ship)

  function rebase() {
    const now = git()

    if (now === null) return

    const files = now.conflicts ?? []

    if (view().dirty === 0 && now.conflicts !== null && files.length === 0) {
      return act('The person pressed Rebase & push', () => steps.rebase(), rebaseAsk(now.base), HOW.rebase)
    }
    // Which side wins differs every time: this one is the agent's from the start.
    if (files.length > 0) toast(`Rebase & push: conflicts likely in ${files.join(', ')}; the agent rebases by hand`)

    return send(rebaseAsk(now.base), HOW.rebase)
  }

  // --- The pieces the band and the full view are made of ----------------------

  // The leader as the host writes it (`ctrl+x`), so a hint reads as what is pressed.
  const leader = (() => {
    try {
      return api.keys.formatBindings(api.tuiConfig.keybinds.get('leader') as never) || 'ctrl+x'
    } catch {
      return 'ctrl+x'
    }
  })()

  const hint = (command: string): string => {
    const key = keys[command]

    return key === undefined || key === 'none' ? '' : ` ${key.replace('<leader>', `${leader} `)}`
  }

  /** A key of the band: a chip that lights up under the mouse, with its chord dim beside the label. */
  const Key = (props: { label: string; command?: string; dim?: boolean; run: () => unknown }) => {
    const [isOver, setOver] = createSignal(false)

    return (
      <box
        flexShrink={0}
        paddingLeft={1}
        paddingRight={1}
        backgroundColor={isOver() ? theme().primary : theme().backgroundElement}
        onMouseOver={() => setOver(true)}
        onMouseOut={() => setOver(false)}
        onMouseUp={() => void props.run()}
      >
        <text fg={isOver() ? theme().selectedListItemText : props.dim ? theme().textMuted : theme().text}>
          {props.dim ? props.label : <b>{props.label}</b>}
          <span style={{ fg: isOver() ? theme().selectedListItemText : theme().textMuted }}>
            {props.command === undefined ? '' : hint(props.command)}
          </span>
        </text>
      </box>
    )
  }

  /** One line of the band: what is the case on the left, its keys in a column on the right. */
  const Row = (props: { children: JSX.Element; keys: JSX.Element }) => (
    <box flexDirection="row" justifyContent="space-between" gap={2}>
      <box flexShrink={1} minWidth={0}>{props.children}</box>
      <box flexDirection="row" flexShrink={0} gap={1}>{props.keys}</box>
    </box>
  )

  const StopKeys = () => (
    <>
      <Key label="Resolve" command="prwatch.resolve" run={resolve} />
      <Key label="Cancel" command="prwatch.cancel" dim run={() => setHalted(null)} />
    </>
  )

  const Stopped = (props: { halt: Halt }) => (
    <Row keys={<StopKeys />}>
      <text fg={theme().error} wrapMode="none" truncate>
        ✗ {props.halt.key} failed · {props.halt.why.split(/\r?\n/).find(line => line.trim() !== '')?.trim() ?? ''}
      </text>
    </Row>
  )

  const PrKeys = (props: { hasHide?: boolean }) => (
    <>
      <Show when={view().failed.length > 0}>
        <Key label="Retry failed" run={retry} />
        <Key label="Hand log to the agent" run={hand} />
      </Show>
      <Key label="Open in browser" dim run={browse} />
      <Show when={props.hasHide}>
        <Key label="Hide" dim run={() => setHiddenPr(keyOf(pr() as PrWatchSnapshot))} />
      </Show>
    </>
  )

  const Band = () => (
    <box flexDirection="column" flexShrink={0} paddingLeft={2} paddingRight={2}>
      <Show when={doing()}>{busy => <text fg={theme().warning} wrapMode="none" truncate>● {busy()}</text>}</Show>
      <Show when={halted()}>{halt => <Stopped halt={halt()} />}</Show>
      <Show when={view().hasPr ? pr() : null}>
        {open => {
          const head = () => HEAD[view().state ?? 'green']
          const skipped = () => open().checks.filter(check => check.state === 'skipped').length
          const issue = () => open().issue

          return (
            <box flexDirection="column">
              <Row keys={<PrKeys hasHide />}>
                <text wrapMode="none" truncate>
                  <span style={{ fg: theme()[head().color], bold: true }}>
                    {head().mark} PR #{open().number}
                  </span>
                  <span style={{ fg: theme()[head().color] }}> {head().word}</span>
                  <span style={{ fg: theme().textMuted }}> · </span>
                  {issue() === null ? open().title : `#${issue()?.number} ${issue()?.title}`}
                  {(issue()?.total ?? 0) > 0 ? ` · ${issue()?.done}/${issue()?.total} done` : ''}
                </text>
              </Row>
              <box flexDirection="row" flexWrap="wrap" columnGap={2} marginLeft={2}>
                <For each={open().checks.filter(check => check.state !== 'skipped')}>
                  {check => (
                    <text>
                      <span style={{ fg: theme()[check.state === 'pass' ? 'success' : check.state === 'fail' ? 'error' : check.state === 'running' ? 'warning' : 'textMuted'] }}>
                        {GLYPH[check.state]}{' '}
                      </span>
                      <span style={{ fg: check.state === 'pass' ? theme().textMuted : theme().text, bold: check.state === 'fail' }}>
                        {check.name.split(' — ')[0]}
                      </span>
                      <span style={{ fg: theme().textMuted }}>
                        {check.state === 'queued' ? ' queued' : check.seconds === null ? '' : ` ${duration(check.seconds)}`}
                      </span>
                    </text>
                  )}
                </For>
                <Show when={skipped() > 0}>
                  <text fg={theme().textMuted}>– {skipped()} skipped</text>
                </Show>
                <Show when={open().checks.length === 0}>
                  <text fg={theme().textMuted}>no checks reported yet</text>
                </Show>
              </box>
              <For each={view().failed}>
                {check => {
                  const log = () => open().logs.find(one => one.jobId === check.jobId)

                  return (
                    <box flexDirection="column" marginLeft={2}>
                      <text fg={theme().error} wrapMode="none" truncate>
                        ✗ {check.name}
                        {log() === undefined ? ' · log arrives when the run finishes' : log()?.step === '' ? '' : ` › ${log()?.step}`}
                      </text>
                      <For each={headline(log()?.lines ?? [], 2)}>
                        {line => <text fg={theme().textMuted} wrapMode="none" truncate>    {line}</text>}
                      </For>
                    </box>
                  )
                }}
              </For>
            </box>
          )
        }}
      </Show>
      <Show when={view().dirty > 0 ? git() : null}>
        {now => (
          <Row keys={<Key label="Commit" command="prwatch.commit" run={commit} />}>
            <text wrapMode="none" truncate>
              <span style={{ fg: theme().info, bold: true }}>● {view().dirty} uncommitted</span>
              <span style={{ fg: theme().textMuted }}>
                {' · '}
                {[now().modified > 0 ? `${now().modified} modified` : '', now().untracked > 0 ? `${now().untracked} new` : '']
                  .filter(Boolean)
                  .join(', ')}
                {view().outgoing > 0 ? ` · ↑ ${view().outgoing} not pushed` : ''}
              </span>
            </text>
          </Row>
        )}
      </Show>
      <Show when={view().hasPush}>
        <Row keys={<Key label={view().isOpen ? 'Push' : view().outgoing > 0 ? 'Push & open PR' : 'Open PR'} command="prwatch.push" run={push} />}>
          <text wrapMode="none" truncate>
            <span style={{ fg: theme().info, bold: true }}>
              {view().outgoing > 0 ? `↑ ${plural(view().outgoing, 'commit')} not pushed` : '↑ pushed'}
            </span>
            <span style={{ fg: theme().textMuted }}>{view().isOpen ? '' : ' · no PR yet'}</span>
          </text>
        </Row>
      </Show>
      <Show when={view().isDone ? git() : null}>
        {now => (
          <Row
            keys={
              <Show when={view().dirty === 0}>
                <Key label={`Switch to ${short(now().base)}`} run={switchToBase} />
              </Show>
            }
          >
            <text wrapMode="none" truncate>
              <span style={{ fg: theme().secondary, bold: true }}>✓ PR #{pr()?.number} merged</span>
              <span style={{ fg: theme().textMuted }}>
                {' · this branch is done'}
                {view().dirty === 0 ? '' : ' · commit or stash first to switch'}
              </span>
            </text>
          </Row>
        )}
      </Show>
      <Show when={view().hasRebase ? git() : null}>
        {now => {
          const files = () => now().conflicts ?? []

          return (
            <Row
              keys={
                <>
                  <Key label="Rebase & push" command="prwatch.rebase" run={rebase} />
                  <Key label="Hide" dim run={() => setHiddenRebase(now().baseSha)} />
                </>
              }
            >
              <text wrapMode="none" truncate>
                <span style={{ fg: theme().warning, bold: true }}>
                  ↓ {now().behind} behind {short(now().base)}
                </span>
                <span style={{ fg: theme().textMuted }}> · </span>
                <span style={{ fg: now().conflicts === null ? theme().textMuted : files().length === 0 ? theme().success : theme().error }}>
                  {now().conflicts === null
                    ? 'conflict check unavailable'
                    : files().length === 0
                      ? 'rebase looks clean'
                      : `conflicts likely in ${plural(files().length, 'file')}: ` +
                        files().slice(0, 2).map(path => path.split('/').pop()).join(', ') +
                        (files().length > 2 ? ', …' : '')}
                </span>
              </text>
            </Row>
          )
        }}
      </Show>
    </box>
  )

  // --- The full view, opened with /pr-watch ------------------------------------

  const Full = () => (
    <box flexDirection="column" paddingLeft={2} paddingRight={2} paddingBottom={1}>
      <Show when={halted()}>
        {halt => (
          <box flexDirection="column" paddingBottom={1}>
            <text fg={theme().error} wrapMode="word">
              <b>✗ {halt().key} failed</b>
              {`\n${halt().why}`}
            </text>
            <box flexDirection="row" gap={1}>
              <StopKeys />
            </box>
          </box>
        )}
      </Show>
      <Show
        when={pr()}
        fallback={<text fg={theme().textMuted}>{isLoaded() ? 'This branch has no pull request.' : 'Looking for the pull request…'}</text>}
      >
        {open => {
          const head = () => HEAD[overall(open())]
          const behind = () => (git()?.branch === open().branch ? (git()?.behind ?? null) : null)
          const skipped = () => open().checks.filter(check => check.state === 'skipped')

          return (
            <box flexDirection="column">
              <text fg={theme()[head().color]} wrapMode="none" truncate>
                <b>
                  {head().mark} PR #{open().number} · {open().title}
                </b>
              </text>
              <text>
                <span style={{ fg: theme().textMuted }}>Status  </span>
                <span style={{ fg: theme()[head().color], bold: true }}>{head().word}</span>
                <span style={{ fg: theme().textMuted }}>{open().isDraft ? ' · draft' : ''}</span>
              </text>
              <Show when={open().issue}>
                {issue => (
                  <text wrapMode="none" truncate>
                    <span style={{ fg: theme().textMuted }}>Issue   </span>#{issue().number} {issue().title}
                    {issue().total > 0 ? ` · ${issue().done}/${issue().total} done` : ''}
                  </text>
                )}
              </Show>
              <text wrapMode="none" truncate>
                <span style={{ fg: theme().textMuted }}>Branch  </span>
                {open().branch}
                <span style={{ fg: (behind() ?? 0) > 0 ? theme().warning : theme().textMuted }}>
                  {behind() === null || open().state !== 'OPEN'
                    ? ''
                    : (behind() ?? 0) > 0
                      ? ` · ${behind()} behind ${short(git()?.base ?? 'base')}`
                      : ' · up to date'}
                </span>
              </text>
              <text> </text>
              <Show when={open().checks.length === 0}>
                <text fg={theme().textMuted}>  No checks reported yet.</text>
              </Show>
              <For each={open().checks.filter(check => check.state !== 'skipped')}>
                {check => {
                  const log = () => open().logs.find(one => one.jobId === check.jobId)

                  return (
                    <box flexDirection="column">
                      <text wrapMode="none" truncate>
                        <span style={{ fg: theme()[check.state === 'pass' ? 'success' : check.state === 'fail' ? 'error' : check.state === 'running' ? 'warning' : 'textMuted'] }}>
                          {'  '}
                          {GLYPH[check.state]}{' '}
                        </span>
                        <span style={{ bold: check.state === 'fail' }}>{check.name}</span>
                        <span style={{ fg: theme().textMuted }}>
                          {check.state === 'running' ? '  running ' : check.state === 'queued' ? '  queued' : '  '}
                          {duration(check.seconds)}
                        </span>
                      </text>
                      <Show when={check.state === 'fail' && log() === undefined}>
                        <text fg={theme().textMuted}>      log arrives when the run finishes</text>
                      </Show>
                      <Show when={log()}>
                        {found => (
                          <box flexDirection="column" marginLeft={6}>
                            <Show when={found().step !== ''}>
                              <text fg={theme().error} wrapMode="none" truncate>{found().step}</text>
                            </Show>
                            <For each={headline(found().lines, 6)}>
                              {line => <text fg={theme().textMuted} wrapMode="none" truncate>{line}</text>}
                            </For>
                          </box>
                        )}
                      </Show>
                    </box>
                  )
                }}
              </For>
              <Show when={skipped().length > 0}>
                <text fg={theme().textMuted} wrapMode="none" truncate>
                  {'  '}– {skipped().length} skipped by path filter: {skipped().map(check => check.name.split(' — ')[0]).join(', ')}
                </text>
              </Show>
              <text> </text>
              <box flexDirection="row" gap={1}>
                <PrKeys />
                <Key label="Refresh" run={refreshPr} />
              </box>
            </box>
          )
        }}
      </Show>
    </box>
  )

  async function open() {
    void refreshPr()
    api.ui.dialog.setSize('large')
    api.ui.dialog.replace(() => <Full />)
  }

  // --- Where it all hangs: the slot above the prompt, the keys, the clock -------

  api.slots.register({
    order: 100,
    slots: {
      // The host's own prompt, with the band on top of it.
      session_prompt(_ctx, props) {
        const Prompt = api.ui.Prompt
        const Slot = api.ui.Slot

        return (
          <box flexDirection="column" flexShrink={0}>
            <Band />
            <Prompt
              sessionID={props.session_id}
              visible={props.visible}
              disabled={props.disabled}
              onSubmit={props.on_submit}
              ref={props.ref}
              right={<Slot name="session_prompt_right" session_id={props.session_id} />}
            />
          </box>
        )
      },
      // The branch rides the prompt's own meta row, dim.
      session_prompt_right() {
        return (
          <Show when={git()}>{now => <text fg={theme().textMuted}>⎇ {now().branch}</text>}</Show>
        )
      },
    },
  })

  const commands = [
    { name: 'prwatch.open', title: 'Open the full view of this branch\'s PR', slashName: 'pr-watch', run: open },
    { name: 'prwatch.commit', title: 'Commit', enabled: () => view().dirty > 0, run: commit },
    { name: 'prwatch.push', title: 'Push, and open a PR when there is none', enabled: () => view().hasPush, run: push },
    { name: 'prwatch.rebase', title: 'Rebase onto the default branch & push', enabled: () => view().hasRebase, run: rebase },
    { name: 'prwatch.resolve', title: 'Resolve the failed key with the agent', enabled: () => halted() !== null, run: resolve },
    { name: 'prwatch.cancel', title: 'Cancel the failed key', enabled: () => halted() !== null, run: () => setHalted(null) },
    { name: 'prwatch.retry', title: 'Retry failed jobs', enabled: () => view().failed.length > 0, run: retry },
    { name: 'prwatch.hand', title: 'Hand the failing log to the agent', enabled: () => view().failed.length > 0, run: hand },
    { name: 'prwatch.web', title: 'Open the PR in the browser', enabled: () => pr() !== null, run: browse },
    { name: 'prwatch.switch', title: 'Switch to the default branch', enabled: () => view().isDone && view().dirty === 0, run: switchToBase },
  ]

  api.keymap.registerLayer({
    mode: 'base',
    commands: commands.map(command => ({
      ...command,
      title: command.name === 'prwatch.open' ? command.title : `pr-watch: ${command.title}`,
      category: 'pr-watch',
      namespace: 'palette',
      run: () => void command.run(),
    })),
    bindings: commands
      .filter(command => keys[command.name] !== undefined && keys[command.name] !== 'none')
      .map(command => ({ key: keys[command.name] as string, cmd: command.name, desc: command.title })),
  })

  let settle: ReturnType<typeof setTimeout> | undefined

  // A file the agent wrote shows as uncommitted at once, not at the next tick.
  api.event.on('file.edited', () => {
    clearTimeout(settle)
    settle = setTimeout(() => void refreshGit(), 400)
  })
  api.event.on('session.idle', () => {
    void refreshGit()
    void refreshPr()
  })

  const clock = setInterval(() => {
    ticks += 1

    if (ticks % FETCH_TICKS === 0) void steps.fetchBase()

    const isLive = last !== null && (last.overall === 'failed' || last.overall === 'running')

    void refreshGit()
    if (isLive || ticks % IDLE_TICKS === 0) void refreshPr()
  }, TICK_MS)

  api.lifecycle.onDispose(() => {
    clearInterval(clock)
    clearTimeout(settle)
  })

  void refreshGit()
  void refreshPr()
}

const plugin: TuiPluginModule & { id: string } = { id: 'kitt.pr-watch', tui }

export default plugin
