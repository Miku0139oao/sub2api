import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountUsageCell from '../AccountUsageCell.vue'
import type { Account } from '@/types'

const { getUsage } = vi.hoisted(() => ({
  getUsage: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getUsage
    }
  }
}))

const MESSAGES: Record<string, string> = {
  'admin.accounts.usageWindow.geminiAppLocalOnly': '24h local',
  'admin.accounts.usageWindow.geminiAppLocalOnlyHint': 'Local usage for the last 24 hours'
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => MESSAGES[key] ?? key
    })
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'gemini_app',
    type: 'oauth',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides
  }
}

describe('AccountUsageCell gemini_app', () => {
  beforeEach(() => {
    getUsage.mockReset()
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: true,
        media: '(min-width: 768px)',
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn()
      }))
    })
  })

  it('renders local 24h request/token/cost chips without an upstream failure', async () => {
    getUsage.mockResolvedValue({
      source: 'local',
      gemini_app_local_usage: {
        requests: 12,
        tokens: 3456,
        cost: 0.42
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({ id: 99001, platform: 'gemini_app', type: 'oauth' })
      },
      global: {
        stubs: {
          UsageProgressBar: true,
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(99001)

    const text = wrapper.text()
    expect(text).toContain('12 req')
    expect(text).toContain('3.5K')
    expect(text).toContain('A $0.42')
    expect(text).toContain('24h local')

    // Gemini App has no upstream quota API, so a successful local-only response
    // must not render the generic usage error message.
    expect(text).not.toContain('common.error')
    expect(wrapper.find('.text-red-500').exists()).toBe(false)
  })
})
