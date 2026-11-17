// The parsing and the types are pr-watch's own (../../pr-watch), shared with the
// Claude Code mod. The package carries a copy, so it installs from npm alone:
// `bun run sync` refreshes it, and tests/shared.test.ts fails when it drifts.
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const here = join(import.meta.dir, '..')
const source = join(here, '..', '..', 'pr-watch')

export const COPIES = [
  { from: join(source, 'hooks', 'parse.ts'), to: join(here, 'shared', 'parse.ts') },
  { from: join(source, 'types', 'index.d.ts'), to: join(here, 'shared', 'types.d.ts') },
]

const HEAD = '// Copied from pr-watch/ by `bun run sync`: change it there, not here.\n'

export const copyOf = (text: string): string => HEAD + text.replace("from '../types'", "from './types'")

if (import.meta.main) {
  for (const { from, to } of COPIES) writeFileSync(to, copyOf(readFileSync(from, 'utf8')))
}
