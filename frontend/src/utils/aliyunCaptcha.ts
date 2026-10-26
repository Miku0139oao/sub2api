import { isChineseLocale, isTraditionalChineseLocale } from '@/i18n/locale'

/**
 * Aliyun CAPTCHA 2.0 language codes are not BCP 47: cn = Simplified Chinese,
 * tw = Traditional Chinese, en = English.
 */
export type AliyunCaptchaLanguage = 'cn' | 'tw' | 'en'

export function aliyunCaptchaLanguage(locale: string | null | undefined): AliyunCaptchaLanguage {
  if (isTraditionalChineseLocale(locale)) return 'tw'
  return isChineseLocale(locale) ? 'cn' : 'en'
}
