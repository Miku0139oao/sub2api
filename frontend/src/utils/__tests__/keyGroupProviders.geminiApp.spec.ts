import { describe, expect, it } from 'vitest'
import {
  getKeyGroupProvider,
  KEY_GROUP_PROVIDER_ICONS,
  KEY_GROUP_PROVIDERS
} from '@/utils/keyGroupProviders'
import en from '@/i18n/locales/en'

describe('keyGroupProviders gemini_app', () => {
  it('buckets gemini_app groups under other', () => {
    expect(getKeyGroupProvider('gemini_app')).toBe('other')
    expect(KEY_GROUP_PROVIDERS).toContain('other')
  })

  it('keeps a representative provider mark for the other bucket', () => {
    // The key-creation UI (KeysView, which hosts UseKeyModal) renders these
    // platform marks next to the provider option.
    expect(KEY_GROUP_PROVIDER_ICONS.other.length).toBeGreaterThan(0)
    expect(KEY_GROUP_PROVIDER_ICONS.other).toContain('gemini')
  })

  it('resolves a non-empty label for the bucket the key modal shows', () => {
    const provider = getKeyGroupProvider('gemini_app')
    // KeysView renders t(`keys.providers.${provider}`) for each bucket.
    const label = (en as { keys?: { providers?: Record<string, string> } }).keys?.providers?.[provider]

    expect(typeof label).toBe('string')
    expect(label?.trim().length).toBeGreaterThan(0)
  })
})
