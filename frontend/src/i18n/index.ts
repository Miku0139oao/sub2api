import { createI18n } from 'vue-i18n'

import {
  DEFAULT_LOCALE,
  LOCALE_STORAGE_KEY,
  isLocaleCode,
  normalizeLocaleCode,
  type LocaleCode,
} from './locale'

type LocaleMessages = Record<string, any>

const localeLoaders: Record<LocaleCode, () => Promise<{ default: LocaleMessages }>> = {
  en: () => import('./locales/en'),
  zh: () => import('./locales/zh'),
  'zh-TW': () => import('./locales/zh-TW'),
}

function getDefaultLocale(): LocaleCode {
  const saved = localStorage.getItem(LOCALE_STORAGE_KEY)
  const savedLocale = normalizeLocaleCode(saved)
  if (savedLocale) {
    return savedLocale
  }

  const browserLanguages = [
    ...(Array.isArray(navigator.languages) ? navigator.languages : []),
    navigator.language,
  ]
  for (const language of browserLanguages) {
    const locale = normalizeLocaleCode(language)
    if (locale) {
      return locale
    }
  }

  return DEFAULT_LOCALE
}

export const i18n = createI18n({
  legacy: false,
  locale: getDefaultLocale(),
  fallbackLocale: DEFAULT_LOCALE,
  messages: {},
  // 禁用 HTML 消息警告 - 引导步骤使用富文本内容（driver.js 支持 HTML）
  // 这些内容是内部定义的，不存在 XSS 风险
  warnHtmlMessage: false,
})

const loadedLocales = new Set<LocaleCode>()

export async function loadLocaleMessages(locale: LocaleCode): Promise<void> {
  if (loadedLocales.has(locale)) {
    return
  }

  const loader = localeLoaders[locale]
  const module = await loader()
  i18n.global.setLocaleMessage(locale, module.default)
  loadedLocales.add(locale)
}

export async function initI18n(): Promise<void> {
  const current = getLocale()
  await loadLocaleMessages(current)
  document.documentElement.setAttribute('lang', current)
}

export async function setLocale(locale: string): Promise<void> {
  const normalized = normalizeLocaleCode(locale)
  if (!normalized || !isLocaleCode(normalized)) {
    return
  }

  await loadLocaleMessages(normalized)
  i18n.global.locale.value = normalized
  localStorage.setItem(LOCALE_STORAGE_KEY, normalized)
  document.documentElement.setAttribute('lang', normalized)

  // 同步更新浏览器页签标题，使其跟随语言切换
  const { resolveRouteDocumentTitle } = await import('@/router/title')
  const { default: router } = await import('@/router')
  const { useAppStore } = await import('@/stores/app')
  const { useAuthStore } = await import('@/stores/auth')
  const { useAdminSettingsStore } = await import('@/stores/adminSettings')
  const route = router.currentRoute.value
  const appStore = useAppStore()
  const authStore = useAuthStore()
  const adminSettingsStore = useAdminSettingsStore()
  const customMenuItems = [
    ...(appStore.cachedPublicSettings?.custom_menu_items ?? []),
    ...(authStore.isAdmin ? adminSettingsStore.customMenuItems : []),
  ]
  document.title = resolveRouteDocumentTitle(route, appStore.siteName, customMenuItems)
}

export function getLocale(): LocaleCode {
  return normalizeLocaleCode(i18n.global.locale.value) ?? DEFAULT_LOCALE
}

export const availableLocales = [
  { code: 'en', name: 'English', flag: '🇺🇸' },
  { code: 'zh', name: '简体中文', flag: '🇨🇳' },
  { code: 'zh-TW', name: '繁體中文', flag: '🇹🇼' },
] as const

export type { LocaleCode }
export default i18n
