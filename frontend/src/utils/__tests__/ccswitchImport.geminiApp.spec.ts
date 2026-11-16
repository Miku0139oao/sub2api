import { describe, expect, it } from 'vitest'
import {
  buildCcSwitchImportDeeplink,
  GEMINI_APP_CC_SWITCH_MODEL,
  resolveCcSwitchImportConfig
} from '@/utils/ccswitchImport'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

describe('ccswitchImport gemini_app', () => {
  it.each(['claude', 'gemini'] as const)(
    'imports Gemini App as a Codex provider for %s clients',
    (clientType) => {
      const config = resolveCcSwitchImportConfig('gemini_app', clientType, 'https://x.test/v1')

      expect(config.app).toBe('codex')
      expect(config.model).toBe('gemini-argon')
      expect(GEMINI_APP_CC_SWITCH_MODEL).toBe('gemini-argon')
      // withV1Endpoint is intentionally not used: withoutTrailingSlashes only
      // removes trailing slashes, so exactly one /v1 is preserved and CC Switch
      // never receives /v1/v1.
      expect(config.endpoint).toBe('https://x.test/v1')
      expect(config.endpoint.endsWith('/v1/')).toBe(false)
    }
  )

  it('strips trailing slashes from the endpoint without duplicating /v1', () => {
    expect(
      resolveCcSwitchImportConfig('gemini_app', 'claude', 'https://x.test/v1/').endpoint
    ).toBe('https://x.test/v1')
    expect(
      resolveCcSwitchImportConfig('gemini_app', 'claude', 'https://x.test/v1').endpoint
    ).not.toMatch(/\/v1\/v1/)
  })

  it('builds a codex deeplink that carries the gemini-argon model', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        baseUrl: 'https://x.test/v1',
        platform: 'gemini_app',
        clientType: 'gemini',
        providerName: 'Sub2API',
        apiKey: 'sk-gemini-app',
        usageScript: 'return true'
      })
    )

    expect(params.get('app')).toBe('codex')
    expect(params.get('endpoint')).toBe('https://x.test/v1')
    expect(params.get('model')).toBe('gemini-argon')
    expect(params.get('configFormat')).toBe('json')

    const config = JSON.parse(atob(params.get('config') || '')) as { config: string }
    expect(config.config).toContain('model = "gemini-argon"')
    expect(config.config).toContain('base_url = "https://x.test/v1"')
  })
})
