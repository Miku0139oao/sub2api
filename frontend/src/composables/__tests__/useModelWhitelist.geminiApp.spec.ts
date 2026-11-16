import { describe, expect, it } from 'vitest'
import {
  getModelsByPlatform,
  getPresetMappingsByPlatform
} from '@/composables/useModelWhitelist'

describe('useModelWhitelist gemini_app', () => {
  it('returns exactly the native Gemini App model aliases', () => {
    expect(getModelsByPlatform('gemini_app')).toEqual([
      'gemini-argon',
      'gemini-hydrogen',
      'gemini-flash-lite'
    ])
  })

  it('offers native-alias preset mappings instead of the Claude defaults', () => {
    const geminiAppMappings = getPresetMappingsByPlatform('gemini_app')

    expect(geminiAppMappings.map((mapping) => mapping.from)).toEqual([
      'gemini-argon',
      'gemini-hydrogen',
      'gemini-flash-lite'
    ])
    expect(geminiAppMappings.map((mapping) => mapping.to)).toEqual([
      'gemini-argon',
      'gemini-hydrogen',
      'gemini-flash-lite'
    ])
    // Preset colours are required by the picker UI.
    for (const mapping of geminiAppMappings) {
      expect(mapping.label).toBeTruthy()
      expect(mapping.color).toBeTruthy()
    }

    // The Claude default list must not leak into this platform.
    expect(geminiAppMappings).not.toBe(getPresetMappingsByPlatform('anthropic'))
    expect(geminiAppMappings.some((mapping) => mapping.from.startsWith('claude-'))).toBe(false)
  })
})
