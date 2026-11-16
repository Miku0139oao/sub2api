import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

const { copyToClipboardMock } = vi.hoisted(() => ({
  copyToClipboardMock: vi.fn().mockResolvedValue(true)
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: copyToClipboardMock
  })
}))

vi.mock('file-saver', () => ({
  saveAs: vi.fn()
}))

import UseKeyModal from '../UseKeyModal.vue'

function mountGeminiAppModal() {
  return mount(UseKeyModal, {
    props: {
      show: true,
      apiKey: 'sk-gemini-app-test',
      baseUrl: 'https://x.test/v1',
      platform: 'gemini_app'
    },
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: { template: '<span />' }
      }
    }
  })
}

function findRoutedCodexConfig(wrapper: ReturnType<typeof mountGeminiAppModal>): string | undefined {
  return wrapper
    .findAll('pre code')
    .map((code) => code.text())
    .find((content) => content.includes('[model_providers.sub2api]'))
}

describe('UseKeyModal gemini_app', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('offers exactly Codex + OpenCode, with Codex active by default', async () => {
    const wrapper = mountGeminiAppModal()
    await nextTick()

    const nav = wrapper.get('nav[aria-label="Client"]')
    const tabs = nav.findAll('button')

    expect(tabs).toHaveLength(2)
    expect(tabs[0].text()).toContain('keys.useKeyModal.cliTabs.codexCli')
    expect(tabs[1].text()).toContain('keys.useKeyModal.cliTabs.opencode')

    // Gemini App only exposes OpenAI-shaped endpoints, so the Claude Code and
    // Gemini CLI tabs must not be offered.
    expect(nav.text()).not.toContain('keys.useKeyModal.cliTabs.claudeCode')
    expect(nav.text()).not.toContain('keys.useKeyModal.cliTabs.geminiCli')
    expect(nav.text()).not.toContain('keys.useKeyModal.cliTabs.grokCli')

    // The defaultClientTab for gemini_app is 'codex'.
    expect(tabs[0].classes()).toContain('border-primary-500')
    expect(tabs[1].classes()).not.toContain('border-primary-500')
  })

  it('keeps gemini-argon as the Codex model when the fetched catalog contains it', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({
        models: [
          { slug: 'gemini-flash-lite' },
          { slug: 'gemini-hydrogen' },
          { slug: 'gemini-argon' }
        ]
      })
    }))

    const wrapper = mountGeminiAppModal()
    await nextTick()

    // The Codex tab is already active, so its generated config renders at once.
    expect(findRoutedCodexConfig(wrapper)).toContain('model = "gemini-argon"')

    await wrapper.get('[data-testid="codex-model-catalog-fetch"]').trigger('click')
    await flushPromises()

    const config = findRoutedCodexConfig(wrapper)
    expect(config).toContain('model = "gemini-argon"')
    expect(config).toContain('review_model = "gemini-argon"')
    expect(config).toContain('base_url = "https://x.test/v1"')
  })
})
