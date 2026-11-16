import { beforeEach, describe, expect, it, vi } from 'vitest'

const { showError } = vi.hoisted(() => ({ showError: vi.fn() }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError })
}))

const MESSAGES: Record<string, string> = {
  'admin.accounts.oauth.geminiApp.failedToGenerateUrl': 'Failed to generate Gemini App auth URL',
  'admin.accounts.oauth.geminiApp.missingExchangeParams': 'Missing code, session ID, or state',
  'admin.accounts.oauth.geminiApp.failedToExchangeCode': 'Failed to exchange Gemini App auth code',
  'admin.accounts.oauth.geminiApp.pleaseEnterRefreshToken': 'Please enter Refresh Token',
  'admin.accounts.oauth.geminiApp.failedToValidateRT': 'Failed to validate Refresh Token'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => MESSAGES[key] ?? key
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    geminiApp: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshGeminiAppToken: vi.fn()
    }
  }
}))

import { useGeminiAppOAuth } from '@/composables/useGeminiAppOAuth'
import { adminAPI } from '@/api/admin'

describe('useGeminiAppOAuth.generateAuthUrl', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('stores the auth url, session, state and redirect uri', async () => {
    vi.mocked(adminAPI.geminiApp.generateAuthUrl).mockResolvedValueOnce({
      auth_url: 'https://accounts.google.com/o/oauth2/v2/auth?...',
      session_id: 'session-1',
      state: 'state-1',
      redirect_uri: 'http://localhost:18765'
    })

    const oauth = useGeminiAppOAuth()
    const ok = await oauth.generateAuthUrl(7)

    expect(ok).toBe(true)
    expect(adminAPI.geminiApp.generateAuthUrl).toHaveBeenCalledWith({ proxy_id: 7 })
    expect(oauth.authUrl.value).toContain('accounts.google.com')
    expect(oauth.sessionId.value).toBe('session-1')
    expect(oauth.state.value).toBe('state-1')
    expect(oauth.redirectUri.value).toBe('http://localhost:18765')
    expect(oauth.loading.value).toBe(false)
  })

  it('omits the proxy when none is configured', async () => {
    vi.mocked(adminAPI.geminiApp.generateAuthUrl).mockResolvedValueOnce({
      auth_url: 'url',
      session_id: 's',
      state: 'st'
    })

    const oauth = useGeminiAppOAuth()
    await oauth.generateAuthUrl(null)

    expect(adminAPI.geminiApp.generateAuthUrl).toHaveBeenCalledWith({})
  })

  it('surfaces the backend detail and returns false on failure', async () => {
    vi.mocked(adminAPI.geminiApp.generateAuthUrl).mockRejectedValueOnce({
      response: { data: { detail: 'OAUTH_PROFILE_REQUIRED' } }
    })

    const oauth = useGeminiAppOAuth()
    const ok = await oauth.generateAuthUrl(null)

    expect(ok).toBe(false)
    expect(oauth.error.value).toBe('OAUTH_PROFILE_REQUIRED')
    expect(showError).toHaveBeenCalledWith('OAUTH_PROFILE_REQUIRED')
    expect(oauth.loading.value).toBe(false)
  })
})

describe('useGeminiAppOAuth.exchangeAuthCode', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('refuses to exchange without a code, session or state', async () => {
    const oauth = useGeminiAppOAuth()

    expect(
      await oauth.exchangeAuthCode({ code: '   ', sessionId: 's', state: 'st' })
    ).toBeNull()
    expect(
      await oauth.exchangeAuthCode({ code: 'code', sessionId: '', state: 'st' })
    ).toBeNull()
    expect(
      await oauth.exchangeAuthCode({ code: 'code', sessionId: 's', state: '' })
    ).toBeNull()
    expect(adminAPI.geminiApp.exchangeCode).not.toHaveBeenCalled()
    expect(oauth.error.value).toBe(MESSAGES['admin.accounts.oauth.geminiApp.missingExchangeParams'])
  })

  it('trims the code and forwards session, state and proxy', async () => {
    vi.mocked(adminAPI.geminiApp.exchangeCode).mockResolvedValueOnce({
      access_token: 'at',
      refresh_token: 'rt'
    })

    const oauth = useGeminiAppOAuth()
    const tokenInfo = await oauth.exchangeAuthCode({
      code: '  4/0Aexample  ',
      sessionId: 'session-1',
      state: 'state-1',
      proxyId: 3
    })

    expect(adminAPI.geminiApp.exchangeCode).toHaveBeenCalledWith({
      session_id: 'session-1',
      state: 'state-1',
      code: '4/0Aexample',
      proxy_id: 3
    })
    expect(tokenInfo?.refresh_token).toBe('rt')
    expect(oauth.loading.value).toBe(false)
  })

  it('reports the upstream failure reason', async () => {
    vi.mocked(adminAPI.geminiApp.exchangeCode).mockRejectedValueOnce({
      response: { data: { detail: 'invalid_grant' } }
    })

    const oauth = useGeminiAppOAuth()
    const tokenInfo = await oauth.exchangeAuthCode({
      code: 'code',
      sessionId: 's',
      state: 'st'
    })

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe('invalid_grant')
    expect(showError).toHaveBeenCalledWith('invalid_grant')
  })
})

describe('useGeminiAppOAuth.validateRefreshToken', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('requires a non-empty refresh token', async () => {
    const oauth = useGeminiAppOAuth()

    expect(await oauth.validateRefreshToken('   ')).toBeNull()
    expect(adminAPI.geminiApp.refreshGeminiAppToken).not.toHaveBeenCalled()
    expect(oauth.error.value).toBe(
      MESSAGES['admin.accounts.oauth.geminiApp.pleaseEnterRefreshToken']
    )
  })

  it('trims the token and forwards the proxy', async () => {
    vi.mocked(adminAPI.geminiApp.refreshGeminiAppToken).mockResolvedValueOnce({
      access_token: 'at'
    })

    const oauth = useGeminiAppOAuth()
    const tokenInfo = await oauth.validateRefreshToken('  1//refresh  ', 9)

    expect(adminAPI.geminiApp.refreshGeminiAppToken).toHaveBeenCalledWith('1//refresh', 9)
    expect(tokenInfo?.access_token).toBe('at')
  })
})

describe('useGeminiAppOAuth.buildCredentials', () => {
  it('normalises numeric expiry and defaults the token type', () => {
    const oauth = useGeminiAppOAuth()

    const credentials = oauth.buildCredentials({
      access_token: 'at',
      refresh_token: 'rt',
      expires_at: 1_900_000_000.9,
      scope: 'https://www.googleapis.com/auth/flexible-api',
      email: 'user@example.com',
      subject: '1234'
    })

    expect(credentials).toEqual({
      access_token: 'at',
      refresh_token: 'rt',
      token_type: 'Bearer',
      expires_at: '1900000000',
      scope: 'https://www.googleapis.com/auth/flexible-api',
      email: 'user@example.com',
      subject: '1234'
    })
  })

  it('keeps a string expiry verbatim and falls back to the submitted RT', () => {
    const oauth = useGeminiAppOAuth()

    const credentials = oauth.buildCredentials(
      { access_token: 'at', expires_at: ' 2026-12-01T00:00:00Z ', token_type: 'bearer' },
      '1//old-refresh'
    )

    expect(credentials.expires_at).toBe('2026-12-01T00:00:00Z')
    expect(credentials.refresh_token).toBe('1//old-refresh')
    expect(credentials.token_type).toBe('bearer')
  })

  it('prefers the freshly returned refresh token over the fallback', () => {
    const oauth = useGeminiAppOAuth()

    const credentials = oauth.buildCredentials(
      { access_token: 'at', refresh_token: ' 1//new  ' },
      '1//old'
    )

    expect(credentials.refresh_token).toBe(' 1//new  ')
  })

  it('leaves expires_at undefined when the upstream omits it', () => {
    const oauth = useGeminiAppOAuth()

    const credentials = oauth.buildCredentials({ access_token: 'at' })

    expect(credentials.expires_at).toBeUndefined()
    expect(credentials.refresh_token).toBeUndefined()
  })
})
