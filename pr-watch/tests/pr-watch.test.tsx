import { expect, test } from 'claude-code/testing'

import { countBoxes, headline, parseChecks, parseLog } from '../hooks/parse'

const RUN = 'https://github.com/o/r/actions/runs/11/job/'
const ROLLUP = [
  { __typename: 'CheckRun', name: 'Mobile — Lint', status: 'COMPLETED', conclusion: 'SUCCESS', startedAt: '2026-10-03T20:00:00Z', completedAt: '2026-10-03T20:03:12Z', detailsUrl: `${RUN}1` },
  { __typename: 'CheckRun', name: 'API — Tests', status: 'COMPLETED', conclusion: 'FAILURE', startedAt: '2026-10-03T20:00:00Z', completedAt: '2026-10-03T20:01:04Z', detailsUrl: `${RUN}2` },
  { __typename: 'CheckRun', name: 'Supabase', status: 'IN_PROGRESS', conclusion: '', startedAt: '2026-10-03T20:00:00Z', detailsUrl: `${RUN}3` },
  { __typename: 'CheckRun', name: 'Web — Lint', status: 'COMPLETED', conclusion: 'SKIPPED', startedAt: '2026-10-03T20:00:00Z', completedAt: '2026-10-03T20:00:00Z', detailsUrl: `${RUN}4` },
]
const LOG = [
  'API — Tests\tRun tests\t2026-10-03T20:01:00.0000000Z ##[group]Run pytest',
  'API — Tests\tRun tests\t2026-10-03T20:01:01.0000000Z FAILED tests/test_db_boundary.py::test_service_db_usage - AssertionError',
  'API — Tests\tRun tests\t2026-10-03T20:01:02.0000000Z ##[error]Process completed with exit code 1.',
].join('\n')
const CONFLICTS = ['tree', 'apps/mobile/src/a.tsx', 'apps/api/app/b.py', 'c.md', '', 'CONFLICT (content)'].join('\n')

const world = (argv: readonly string[]): { exitCode: number; stdout: string } => {
  const line = argv.join(' ')
  const ok = (stdout: string) => ({ exitCode: 0, stdout })

  if (line.startsWith('gh pr view --json')) {
    return ok(
      JSON.stringify({
        number: 375,
        title: 'fix(mobile): long step descriptions (#317)',
        body: '',
        state: 'OPEN',
        isDraft: false,
        headRefOid: 'abc',
        headRefName: 'agent/feature',
        closingIssuesReferences: [{ number: 317 }],
        statusCheckRollup: ROLLUP,
      }),
    )
  }
  if (line.startsWith('gh issue view')) {
    return ok(JSON.stringify({ title: 'Long step descriptions', body: '- [x] a\n- [ ] b' }))
  }
  if (line.startsWith('gh run view')) return ok(LOG)
  if (line.startsWith('git symbolic-ref')) return ok('origin/main\n')
  if (line === 'git branch --show-current') return ok('agent/feature\n')
  if (line === 'git status --porcelain') return ok(' M a.ts\n M b.ts\n?? c.ts\n')
  if (line.startsWith('git rev-list --left-right')) return ok('2\t3\n')
  if (line.startsWith('git rev-list --count')) return ok('1\n')
  if (line.startsWith('git rev-parse')) return ok('f00\n')
  if (line.startsWith('git merge-tree')) return { exitCode: 1, stdout: CONFLICTS }

  return ok('')
}

test('checks are read from the rollup', async () => {
  const checks = parseChecks(ROLLUP, Date.parse('2026-10-03T20:00:41Z'))

  expect(checks.map(check => check.state)).toEqual(['pass', 'fail', 'running', 'skipped'])
  expect(checks[0]?.seconds).toBe(192)
  expect(checks[2]?.seconds).toBe(41)
  expect(checks[1]?.jobId).toBe('2')
})

test('a failed log keeps the telling line and its step', async () => {
  const log = parseLog(LOG)

  expect(log.step).toBe('Run tests')
  expect(headline(log.lines, 3)).toEqual([
    'FAILED tests/test_db_boundary.py::test_service_db_usage - AssertionError',
  ])
  expect(countBoxes('- [x] a\n- [ ] b\n* [X] c')).toEqual({ done: 2, total: 3 })
})

for (const surface of ['terminal', 'desktop'] as const) {
  test(`${surface}: the band shows the PR, uncommitted work and the rebase, each with its key`, async ($, on) => {
    const ran: string[] = []
    const sent: string[] = []

    on('process.run', (_, e) => {
      ran.push(e.argv.join(' '))

      return { value: { stderr: '', isStdoutTruncated: false, isStderrTruncated: false, ...world(e.argv) } }
    })
    on('ui.open', () => ({ value: { isPlaced: true as const } }))
    on('prompt.submit', (_, e) => {
      sent.push(e.text)

      return {} as never
    })
    on('tool.call', () => ({ text: '' }) as never)

    // A command going by is one of the moments the mod reads the branch again.
    await $.command.run({ command: 'pr-watch' } as never)
    await $.tool.call({ tool: 'Bash', command: 'git commit -m x' } as never)

    const band = await $.ui.mount({
      plugin: 'pr-watch',
      surface,
      component: 'AbovePrompt',
      props: { hasSurvey: false, isWorking: false, maxRows: 12, bodyColumns: 120 } as never,
    })

    expect(await band.find({ type: 'Text', text: /PR #375/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /#317 Long step descriptions · 1\/2 done/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /API — Tests › Run tests/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /test_service_db_usage/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /3 uncommitted/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /2 modified, 1 new · ↑ 1 not pushed/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /3 behind main/ })).toBeDefined()
    expect(await band.find({ type: 'Text', text: /conflicts likely in 3 files: a\.tsx, b\.py, …/ })).toBeDefined()

    await band.press({ key: 'retry' })
    expect(ran).toContain('gh run rerun 11 --failed')

    await band.press({ key: 'commit' })
    expect(sent.at(-1)).toBe('Commit my changes.')

    await band.press({ key: 'rebase' })
    expect(sent.at(-1)).toBe('Rebase this branch onto main and push the branch.')

    await band.press({ key: 'hide-pr' })
    await band.press({ key: 'hide-rebase' })
    expect(await band.find({ type: 'Text', text: /PR #375/ })).toBeUndefined()
    expect(await band.find({ type: 'Text', text: /behind main/ })).toBeUndefined()
    expect(await band.find({ type: 'Text', text: /3 uncommitted/ })).toBeDefined()
    await band.unmount()

    const pane = await $.ui.mount({
      plugin: 'pr-watch',
      surface,
      component: 'Pane',
      requestId: 'pr-watch',
      props: { title: 'PR #375', isFocused: true, bodyColumns: 80, placement: 'inline' } as never,
    })

    expect(await pane.find({ type: 'Text', text: /1 skipped by path filter: Web/ })).toBeDefined()
    expect(await pane.find({ type: 'Text', text: /3 behind main/ })).toBeDefined()
    await pane.unmount()
  })
}
