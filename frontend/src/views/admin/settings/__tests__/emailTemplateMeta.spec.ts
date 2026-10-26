import { describe, expect, it } from 'vitest'

import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import zhTW from '@/i18n/locales/zh-TW'
import { CATEGORY_KEYS, DEFAULT_CATEGORY_KEY, EVENT_META_KEYS } from '../emailTemplateMeta'

function lookup(messages: Record<string, unknown>, key: string): unknown {
  let current: unknown = messages
  for (const segment of key.split('.')) {
    if (!current || typeof current !== 'object') return undefined
    current = (current as Record<string, unknown>)[segment]
  }
  return current
}

describe('email template event metadata keys', () => {
  const keys = new Set<string>([DEFAULT_CATEGORY_KEY, ...Object.values(CATEGORY_KEYS)])
  for (const meta of Object.values(EVENT_META_KEYS)) {
    keys.add(meta.labelKey)
    keys.add(meta.timingKey)
    keys.add(meta.categoryKey)
  }

  it.each([
    ['en', en],
    ['zh', zh],
    ['zh-TW', zhTW],
  ] as const)('%s has a non-empty message for every metadata key', (locale, messages) => {
    const missing = [...keys].filter((key) => {
      const value = lookup(messages as Record<string, unknown>, key)
      return typeof value !== 'string' || value.trim() === ''
    })
    expect(missing, `${locale} is missing email-template metadata keys`).toEqual([])
  })
})
