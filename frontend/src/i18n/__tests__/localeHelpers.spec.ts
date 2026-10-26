import { describe, expect, it } from 'vitest'

import {
  isChineseLocale,
  isLocaleCode,
  isTraditionalChineseLocale,
  normalizeLocaleCode,
  toIntlLocale,
} from '../locale'

describe('locale helpers', () => {
  it('normalizes BCP 47 tags to supported locale codes', () => {
    const cases: Array<[unknown, string | null]> = [
      ['en', 'en'],
      ['en-US', 'en'],
      ['EN_us', 'en'],
      ['zh', 'zh'],
      ['zh-CN', 'zh'],
      ['zh-Hans', 'zh'],
      ['zh-Hans-CN', 'zh'],
      ['zh-SG', 'zh'],
      ['zh-TW', 'zh-TW'],
      ['zh-tw', 'zh-TW'],
      ['zh_TW', 'zh-TW'],
      ['zh-Hant', 'zh-TW'],
      ['zh-Hant-TW', 'zh-TW'],
      ['zh-HK', 'zh-TW'],
      ['zh-MO', 'zh-TW'],
      ['fr', null],
      ['', null],
      [null, null],
      [undefined, null],
      [42, null],
    ]
    for (const [input, expected] of cases) {
      expect(normalizeLocaleCode(input), String(input)).toBe(expected)
    }
  })

  it('validates locale codes exactly', () => {
    expect(isLocaleCode('zh-TW')).toBe(true)
    expect(isLocaleCode('zh-tw')).toBe(false)
    expect(isLocaleCode('zh-Hant')).toBe(false)
    expect(isLocaleCode('fr')).toBe(false)
  })

  it('classifies Chinese locales', () => {
    expect(isChineseLocale('zh')).toBe(true)
    expect(isChineseLocale('zh-CN')).toBe(true)
    expect(isChineseLocale('zh-TW')).toBe(true)
    expect(isChineseLocale('zh-HK')).toBe(true)
    expect(isChineseLocale('en')).toBe(false)
    expect(isChineseLocale(null)).toBe(false)
  })

  it('classifies Traditional Chinese locales', () => {
    expect(isTraditionalChineseLocale('zh-TW')).toBe(true)
    expect(isTraditionalChineseLocale('zh-HK')).toBe(true)
    expect(isTraditionalChineseLocale('zh-Hant')).toBe(true)
    expect(isTraditionalChineseLocale('zh-CN')).toBe(false)
    expect(isTraditionalChineseLocale('zh')).toBe(false)
    expect(isTraditionalChineseLocale('en')).toBe(false)
  })

  it('maps app locales to Intl locales', () => {
    expect(toIntlLocale('zh-TW')).toBe('zh-TW')
    expect(toIntlLocale('zh-HK')).toBe('zh-TW')
    expect(toIntlLocale('zh-CN')).toBe('zh-CN')
    expect(toIntlLocale('zh')).toBe('zh-CN')
    expect(toIntlLocale('en-US')).toBe('en-US')
    expect(toIntlLocale(null)).toBe('en-US')
    expect(toIntlLocale('fr')).toBe('en-US')
  })
})
