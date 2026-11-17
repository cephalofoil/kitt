import { expect, test } from 'bun:test'
import { existsSync, readFileSync } from 'node:fs'

import { copyOf, COPIES } from '../scripts/sync'

// Only in the kitt repo: the published package has no ../../pr-watch next to it.
test.skipIf(!existsSync(COPIES[0]!.from))('shared/ is the copy of pr-watch/ (bun run sync)', () => {
  for (const { from, to } of COPIES) expect(readFileSync(to, 'utf8')).toBe(copyOf(readFileSync(from, 'utf8')))
})
