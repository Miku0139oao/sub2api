/**
 * Locale code helpers.
 *
 * This module is intentionally free of Vue/vue-i18n imports so it can be used
 * from stores, utilities, scripts and tests without creating an import cycle
 * with `./index.ts`.
 */

export type LocaleCode = 'en' | 'zh' | 'zh-TW'

export const DEFAULT_LOCALE: LocaleCode = 'en'
export const LOCALE_STORAGE_KEY = 'sub2api_locale'

export function isLocaleCode(value: unknown): value is LocaleCode {
  return value === 'en' || value === 'zh' || value === 'zh-TW'
}

/**
 * Normalize an arbitrary BCP 47-ish tag to a supported locale code.
 *
 * - en / en-*                 -> en
 * - zh, zh-CN, zh-SG, zh-Hans -> zh (Simplified)
 * - zh-Hant*, zh-TW/HK/MO     -> zh-TW
 * - anything else             -> null
 */
export function normalizeLocaleCode(value: unknown): LocaleCode | null {
  if (typeof value !== 'string') return null
  const tag = value.trim().toLowerCase().replace(/_/g, '-')
  if (!tag) return null
  if (tag === 'en' || tag.startsWith('en-')) return 'en'
  if (tag === 'zh' || tag.startsWith('zh-')) {
    if (
      tag.includes('hant')
      || /(?:^|-)tw(?:-|$)/.test(tag)
      || /(?:^|-)hk(?:-|$)/.test(tag)
      || /(?:^|-)mo(?:-|$)/.test(tag)
    ) {
      return 'zh-TW'
    }
    return 'zh'
  }
  return null
}

export function isChineseLocale(locale: string | null | undefined): boolean {
  const code = normalizeLocaleCode(locale)
  return code === 'zh' || code === 'zh-TW'
}

export function isTraditionalChineseLocale(locale: string | null | undefined): boolean {
  return normalizeLocaleCode(locale) === 'zh-TW'
}

export function toIntlLocale(locale: string | null | undefined): 'en-US' | 'zh-CN' | 'zh-TW' {
  const code = normalizeLocaleCode(locale)
  if (code === 'zh-TW') return 'zh-TW'
  if (code === 'zh') return 'zh-CN'
  return 'en-US'
}
