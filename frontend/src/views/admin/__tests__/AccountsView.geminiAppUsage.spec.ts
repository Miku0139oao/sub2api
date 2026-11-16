import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const currentDir = dirname(fileURLToPath(import.meta.url))
const accountsViewSource = readFileSync(resolve(currentDir, '../AccountsView.vue'), 'utf8')

describe('AccountsView gemini_app batch usage', () => {
  it('includes gemini_app oauth accounts in batch usage fetching', () => {
    const match = accountsViewSource.match(
      /const accountSupportsBatchUsage = \(account: Account\) => \{([\s\S]*?)\n\}/
    )

    expect(match).not.toBeNull()
    // Gemini App usage is local-only, but oauth accounts still expose a batched
    // usage endpoint and must be polled like the other oauth platforms.
    expect(match?.[1]).toContain(
      "if (account.platform === 'gemini_app') return account.type === 'oauth'"
    )
  })
})
