import { describe, expect, it } from 'vitest'
import {
  platformAccentBarClass,
  platformAccentColor,
  platformBadgeClass,
  platformBadgeLightClass,
  platformBorderClass,
  platformBorderStrongClass,
  platformButtonClass,
  platformDiscountClass,
  platformGradientClass,
  platformGradientSubtextClass,
  platformGradientTextClass,
  platformIconClass,
  platformLabel,
  platformTextClass
} from '@/utils/platformColors'
import { getPlatformSpec } from '@/constants/platformCatalog'

// `isPlatform()` in platformColors is module-private, so "gemini_app is a
// registered platform" is asserted through the public accessors: each one takes
// the platform-specific lime branch instead of the neutral default branch.
describe('platformColors gemini_app', () => {
  it('returns non-empty, gemini_app-specific class strings from every color map', () => {
    const classes = [
      platformBadgeClass('gemini_app'),
      platformBadgeLightClass('gemini_app'),
      platformBorderClass('gemini_app'),
      platformBorderStrongClass('gemini_app'),
      platformAccentBarClass('gemini_app'),
      platformTextClass('gemini_app'),
      platformIconClass('gemini_app'),
      platformButtonClass('gemini_app'),
      platformDiscountClass('gemini_app'),
      platformGradientClass('gemini_app'),
      platformGradientTextClass('gemini_app'),
      platformGradientSubtextClass('gemini_app')
    ]

    for (const value of classes) {
      expect(typeof value).toBe('string')
      expect(value.trim().length).toBeGreaterThan(0)
      // The lime family is only used by gemini_app in these maps; seeing it
      // proves the gemini_app entry (rather than a *_DEFAULT fallback) is used.
      expect(value).toContain('lime')
    }
  })

  it('uses the lime accent color consistently with the class maps', () => {
    expect(platformAccentColor('gemini_app')).toBe('#84cc16')
    expect(platformBadgeClass('gemini_app')).toContain('lime')
    expect(platformBorderClass('gemini_app')).toContain('lime')
    expect(platformTextClass('gemini_app')).toContain('lime')
  })

  it('labels gemini_app as "Gemini App"', () => {
    expect(platformLabel('gemini_app')).toBe('Gemini App')
  })

  it('registers gemini_app in the platform catalog with the openai gateway', () => {
    const spec = getPlatformSpec('gemini_app')

    expect(spec).toBeDefined()
    expect(spec?.id).toBe('gemini_app')
    expect(spec?.display_name).toBe('Gemini App')
    expect(spec?.gateway).toBe('openai')
  })
})
