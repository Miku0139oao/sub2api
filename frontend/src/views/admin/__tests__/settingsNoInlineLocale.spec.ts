import { describe, expect, it } from 'vitest'

const sources = import.meta.glob('/src/**/*.vue', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

const TARGETS = [
  '/src/views/admin/SettingsView.vue',
  '/src/views/admin/settings/EmailTemplateEditor.vue',
  '/src/components/auth/WechatOAuthSection.vue',
]

function sourceFor(target: string): string {
  const source = sources[target]
  if (!source) throw new Error(`missing source for ${target}`)
  return source
}

describe('no inline zh/en locale bypasses', () => {
  it.each(TARGETS)('%s uses the i18n catalog', (target) => {
    const source = sourceFor(target)
    expect(source).not.toContain('localText(')
    expect(source).not.toMatch(/locale\.value\s*\.toLowerCase\(\)\s*\.startsWith\(\s*["']zh["']\s*\)/)
  })
})
