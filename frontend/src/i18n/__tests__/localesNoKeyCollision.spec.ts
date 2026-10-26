import { describe, expect, it } from 'vitest'

// locales/{zh,en,zh-TW}/index.ts 与 admin/index.ts 使用对象展开聚合各域模块，
// 展开模块之间若出现同名顶层键会静默覆盖。本测试将该风险固化为显式失败。
type ModuleMap = Record<string, { default: Record<string, unknown> }>

const modules = import.meta.glob('../locales/**/*.ts', {
  eager: true,
}) as ModuleMap

// adminAccessKeys is imported as a named namespace (not spread), so it cannot
// collide with the spread root modules.
const NON_SPREAD_ROOT_MODULES = new Set(['index.ts', 'adminAccessKeys.ts'])

function collisions(modules: Array<[string, Record<string, unknown>]>): string[] {
  const seen = new Map<string, string>()
  const out: string[] = []
  for (const [name, mod] of modules) {
    for (const key of Object.keys(mod)) {
      const prev = seen.get(key)
      if (prev) {
        out.push(`"${key}" in both ${prev} and ${name}`)
      } else {
        seen.set(key, name)
      }
    }
  }
  return out
}

const localeModules = new Map<string, { roots: Array<[string, Record<string, unknown>]>; admins: Array<[string, Record<string, unknown>]> }>()

for (const [path, module] of Object.entries(modules)) {
  const match = /\/locales\/([^/]+)\/(.+)$/.exec(path)
  if (!match) continue
  const [, locale, relative] = match
  if (!localeModules.has(locale)) localeModules.set(locale, { roots: [], admins: [] })
  const entry = localeModules.get(locale)!
  if (relative.startsWith('admin/')) {
    if (relative !== 'admin/index.ts') entry.admins.push([relative, module.default])
    continue
  }
  if (NON_SPREAD_ROOT_MODULES.has(relative)) continue
  entry.roots.push([relative, module.default])
}

describe.each([...localeModules.keys()].sort())('locale %s spread assembly', (locale) => {
  const entry = localeModules.get(locale)!

  it('root modules have no overlapping top-level keys', () => {
    expect(collisions(entry.roots)).toEqual([])
  })

  it('root modules do not shadow the explicit "admin" namespace', () => {
    for (const [name, mod] of entry.roots) {
      expect(Object.keys(mod), `module ${name} must not define "admin"`).not.toContain('admin')
    }
  })

  it('admin modules have no overlapping top-level keys', () => {
    expect(collisions(entry.admins)).toEqual([])
  })
})
