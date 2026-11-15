/** @jsxImportSource @opentui/solid */
import { afterAll, beforeAll, expect, test } from 'bun:test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import type { TuiPluginApi } from '@opencode-ai/plugin/tui'
import { RGBA } from '@opentui/core'
import { testRender } from '@opentui/solid'

import plugin from '../tui'

// The band is drawn from a real repository in a temp folder; opencode itself is
// stood in for by the few parts of its API the plugin touches.

let repo = ''
const git = (...argv: string[]) => Bun.spawnSync(['git', ...argv], { cwd: repo }).stdout.toString().trim()

type Sent = { sessionID: string; noReply?: boolean; tools?: Record<string, boolean>; parts: { text: string; synthetic?: boolean }[] }

function host(answer: () => string) {
  const slots: Record<string, (ctx: unknown, props: Record<string, unknown>) => unknown> = {}
  const commands = new Map<string, { run: () => void }>()
  const prompts: Sent[] = []
  const turns: Sent[] = []
  const deleted: string[] = []
  const disposers: (() => void)[] = []
  const color = RGBA.fromHex('#cccccc')
  const api = {
    state: { path: { directory: repo }, session: { status: () => undefined } },
    route: { current: { name: 'session', params: { sessionID: 's1' } } },
    keys: { formatBindings: () => undefined },
    tuiConfig: { keybinds: { get: () => [] } },
    theme: { current: new Proxy({}, { get: () => color }) },
    ui: {
      toast: () => {},
      Prompt: () => <text>PROMPT</text>,
      Slot: (props: { name: string }) => slots[props.name]?.({}, props) ?? null,
      dialog: { setSize: () => {}, replace: () => {} },
    },
    slots: { register: (one: { slots: typeof slots }) => (Object.assign(slots, one.slots), 'id') },
    keymap: { registerLayer: (layer: { commands: { name: string; run: () => void }[] }) => layer.commands.forEach(one => commands.set(one.name, one)) },
    event: { on: () => () => {} },
    lifecycle: { onDispose: (fn: () => void) => disposers.push(fn) },
    client: {
      session: {
        fork: async () => ({ data: { id: 'fork1' } }),
        prompt: async (sent: Sent) => (prompts.push(sent), { data: { parts: [{ type: 'text', text: sent.noReply ? '' : answer() }] } }),
        promptAsync: async (sent: Sent) => void turns.push(sent),
        delete: async (sent: { sessionID: string }) => void deleted.push(sent.sessionID),
      },
    },
  }

  return { api: api as unknown as TuiPluginApi, slots, commands, prompts, turns, deleted, dispose: () => disposers.forEach(fn => fn()) }
}

async function shown(render: Awaited<ReturnType<typeof testRender>>, wanted: RegExp): Promise<string> {
  let frame = ''

  for (let tries = 0; tries < 100; tries += 1) {
    await render.renderOnce()
    frame = render.captureCharFrame()
    if (wanted.test(frame)) return frame
    await Bun.sleep(50)
  }

  throw new Error(`never shown: ${wanted}\n${frame}`)
}

beforeAll(() => {
  repo = mkdtempSync(join(tmpdir(), 'pr-watch-'))
  git('init', '-q', '-b', 'main')
  git('config', 'user.email', 'test@example.com')
  git('config', 'user.name', 'Test')
  git('commit', '-q', '--allow-empty', '-m', 'first')
  git('switch', '-q', '-c', 'feat/band')
})

afterAll(() => rmSync(repo, { recursive: true, force: true }))

test('a commit by key: the message comes from a fork with no tools, the agent gets a note, no turn starts', async () => {
  writeFileSync(join(repo, 'a.txt'), 'a\n')

  const on = host(() => 'feat: add a\n\nThe first file.')

  await plugin.tui(on.api, undefined, {} as never)

  const render = await testRender(() => on.slots.session_prompt?.({}, { session_id: 's1' }) as never, { width: 100, height: 12 })
  const before = await shown(render, /1 uncommitted/)

  expect(before).toContain('Commit ctrl+x i')
  expect(before).toContain('PROMPT')

  on.commands.get('prwatch.commit')?.run()
  await shown(render, /^(?![\s\S]*uncommitted)/)

  expect(git('log', '-1', '--format=%s|%b')).toBe('feat: add a|The first file.')
  expect(on.prompts[0]).toMatchObject({ sessionID: 'fork1', tools: { '*': false } })
  expect(on.prompts[0]?.parts[0]?.text).toContain('a.txt')
  expect(on.deleted).toEqual(['fork1'])
  expect(on.prompts[1]).toMatchObject({ sessionID: 's1', noReply: true })
  expect(on.prompts[1]?.parts[0]?.text).toMatch(/^\[pr-watch\] The person pressed Commit: Committed \w+ feat: add a/)
  expect(on.turns).toEqual([])

  on.dispose()
  render.renderer.destroy()
})

test('a key that stops is one line with two keys; resolving sends its few words, the reason unseen', async () => {
  writeFileSync(join(repo, 'b.txt'), 'b\n')

  const on = host(() => 'ASK: b.txt holds what looks like a token.')

  await plugin.tui(on.api, undefined, {} as never)

  const render = await testRender(() => on.slots.session_prompt?.({}, { session_id: 's1' }) as never, { width: 100, height: 12 })

  await shown(render, /1 uncommitted/)
  on.commands.get('prwatch.commit')?.run()

  const stopped = await shown(render, /Commit failed/)

  expect(stopped).toContain('✗ Commit failed · b.txt holds what looks like a token.')
  expect(stopped).toContain('Resolve ctrl+x f')
  expect(git('status', '--porcelain')).toBe('?? b.txt')
  expect(on.turns).toEqual([])

  on.commands.get('prwatch.resolve')?.run()
  await shown(render, /^(?![\s\S]*Commit failed)/)

  expect(on.turns).toHaveLength(1)
  expect(on.turns[0]?.parts.map(part => part.synthetic === true)).toEqual([false, true, true])
  expect(on.turns[0]?.parts[0]?.text).toBe('Commit my changes.')
  expect(on.turns[0]?.parts[2]?.text).toContain('b.txt holds what looks like a token.')

  on.dispose()
  render.renderer.destroy()
})
