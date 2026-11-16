import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(process.cwd(), 'src/components/account/CreateAccountModal.vue'),
  'utf8'
)

// Gemini App 只有 OAuth 一种凭证类型，模型限制区域必须和 OpenAI/Grok 一样可用，
// 否则创建出来的账号无法配置 model_mapping（只有内置别名可用）。
describe('CreateAccountModal Gemini App model restriction', () => {
  it('renders the model restriction section for gemini_app OAuth accounts', () => {
    expect(source).toContain(
      "(form.platform === 'openai' || form.platform === 'grok' || form.platform === 'gemini_app') && isOAuthFlow"
    )
  })

  it('persists model_mapping for gemini_app when creating the account', () => {
    expect(source).toContain("if (platform === 'grok' || platform === 'gemini_app') {")
    expect(source).toContain('buildModelMappingObject(')
  })
})
