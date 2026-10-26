import { describe, expect, it } from 'vitest'

import { adminComplianceDocumentUrl, adminComplianceMarkdown } from '../legalDocuments'

describe('admin compliance legal documents', () => {
  it('selects the Traditional document for zh-TW / zh-HK / zh-Hant', () => {
    for (const locale of ['zh-TW', 'zh-HK', 'zh-Hant', 'zh-Hant-TW']) {
      const markdown = adminComplianceMarkdown(locale)
      expect(markdown).toContain('部署與營運合規承諾')
      expect(markdown).not.toContain('部署与运营合规承诺')
    }
  })

  it('keeps Simplified and English documents for their locales', () => {
    expect(adminComplianceMarkdown('zh')).toContain('部署与运营合规承诺')
    expect(adminComplianceMarkdown('zh-CN')).toContain('部署与运营合规承诺')
    expect(adminComplianceMarkdown('en')).toContain('Sub2API Deployment and Operation Compliance Commitment')
    expect(adminComplianceMarkdown('fr')).toContain('Sub2API Deployment and Operation Compliance Commitment')
  })

  it('resolves the document URL by locale and prefers backend values', () => {
    expect(adminComplianceDocumentUrl({ document_url_zh_tw: 'https://example.com/tw' }, 'zh-TW')).toBe(
      'https://example.com/tw',
    )
    expect(adminComplianceDocumentUrl({ document_url_zh_tw: '' }, 'zh-TW')).toContain('zh-TW.md')
    expect(adminComplianceDocumentUrl({ document_url_zh: 'https://example.com/zh' }, 'zh')).toBe(
      'https://example.com/zh',
    )
    expect(adminComplianceDocumentUrl({ document_url_en: 'https://example.com/en' }, 'en')).toBe(
      'https://example.com/en',
    )
    expect(adminComplianceDocumentUrl(null, 'en')).toContain('admin-compliance.en.md')
  })
})
