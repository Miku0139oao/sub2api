import type { AdminComplianceStatus } from '@/api/admin/compliance'

import { isChineseLocale, isTraditionalChineseLocale } from './locale'

import zhHantDocument from '../../../docs/legal/admin-compliance.zh-TW.md?raw'
import zhDocument from '../../../docs/legal/admin-compliance.zh.md?raw'
import enDocument from '../../../docs/legal/admin-compliance.en.md?raw'

const FALLBACK_URL_ZH =
  'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.zh.md'
const FALLBACK_URL_ZHTW =
  'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.zh-TW.md'
const FALLBACK_URL_EN =
  'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.en.md'

export function adminComplianceMarkdown(locale: string | null | undefined): string {
  if (isTraditionalChineseLocale(locale)) return zhHantDocument
  if (isChineseLocale(locale)) return zhDocument
  return enDocument
}

export function adminComplianceDocumentUrl(
  status: Partial<AdminComplianceStatus> | null | undefined,
  locale: string | null | undefined,
): string {
  if (isTraditionalChineseLocale(locale)) {
    return status?.document_url_zh_tw || FALLBACK_URL_ZHTW
  }
  if (isChineseLocale(locale)) {
    return status?.document_url_zh || FALLBACK_URL_ZH
  }
  return status?.document_url_en || FALLBACK_URL_EN
}
