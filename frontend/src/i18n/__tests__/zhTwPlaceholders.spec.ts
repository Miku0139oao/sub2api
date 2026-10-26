import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'
import zhTW from '../locales/zh-TW'

type LocaleValue = Record<string, unknown>

function flattenLeaves(value: unknown, prefix = '', out = new Map<string, string>()): Map<string, string> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    if (prefix) out.set(prefix, String(value))
    return out
  }
  for (const [key, child] of Object.entries(value as LocaleValue)) {
    flattenLeaves(child, prefix ? `${prefix}.${key}` : key, out)
  }
  return out
}

function matches(value: string, pattern: RegExp): string[] {
  return [...value.matchAll(pattern)].map((match) => match[0]).sort()
}

const CJK_RE = /[㐀-䶿一-鿿豈-﫿]/
const PLACEHOLDER_RE = /\{[A-Za-z0-9_]+\}/g
const LINKED_MESSAGE_RE = /@:[\w.]+/g
const URL_RE = /https?:\/\/[A-Za-z0-9\-._~:/?#[\]@!$&()*+,;=%]+/g
const HTML_TAG_RE = /<\/?[A-Za-z][^>]*>/g

describe('zh-TW message integrity', () => {
  const enLeaves = flattenLeaves(en)
  const zhLeaves = flattenLeaves(zh)
  const zhTWLeaves = flattenLeaves(zhTW)

  it('keeps every zh key in zh-TW', () => {
    expect([...zhLeaves.keys()].filter((key) => !zhTWLeaves.has(key))).toEqual([])
    expect([...zhTWLeaves.keys()].filter((key) => !zhLeaves.has(key))).toEqual([])
    expect([...enLeaves.keys()].filter((key) => !zhTWLeaves.has(key))).toEqual([])
  })

  it('keeps vue-i18n placeholders identical to zh and en', () => {
    const mismatches: string[] = []
    for (const [key, value] of zhLeaves) {
      const zhPlaceholders = matches(value, PLACEHOLDER_RE).join(',')
      const enPlaceholders = [...new Set(matches(enLeaves.get(key) ?? '', PLACEHOLDER_RE))].join(',')
      const zhTWPlaceholders = matches(zhTWLeaves.get(key) ?? '', PLACEHOLDER_RE).join(',')
      if (zhTWPlaceholders !== zhPlaceholders || zhTWPlaceholders !== enPlaceholders) {
        mismatches.push(`${key}: zh=[${zhPlaceholders}] en=[${enPlaceholders}] zh-TW=[${zhTWPlaceholders}]`)
      }
    }
    expect(mismatches).toEqual([])
  })

  it('keeps URLs, HTML tags, linked messages and newlines intact', () => {
    const mismatches: string[] = []
    for (const [key, value] of zhLeaves) {
      const candidate = zhTWLeaves.get(key) ?? ''
      const parts: Array<[string, (text: string) => string[]]> = [
        ['url', (text) => matches(text, URL_RE)],
        ['html', (text) => matches(text, HTML_TAG_RE)],
        ['linked', (text) => matches(text, LINKED_MESSAGE_RE)],
        ['newlines', (text) => (text.match(/\n/g) ?? []).map(() => '\\n')],
      ]
      for (const [name, extract] of parts) {
        const expected = extract(value).join('|')
        const actual = extract(candidate).join('|')
        if (expected !== actual) {
          mismatches.push(`${key} (${name}): zh=[${expected}] zh-TW=[${actual}]`)
        }
      }
    }
    expect(mismatches).toEqual([])
  })

  it('keeps Chinese text Chinese when the zh source is Chinese', () => {
    const mismatches = [...zhLeaves.entries()]
      .filter(([, value]) => CJK_RE.test(value))
      .filter(([key]) => !CJK_RE.test(zhTWLeaves.get(key) ?? ''))
      .map(([key]) => key)
    expect(mismatches).toEqual([])
  })
})
