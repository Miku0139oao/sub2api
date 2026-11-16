import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post }
}))

import geminiAppAPI from '@/api/admin/geminiApp'

describe('admin Gemini App OAuth API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('reads capabilities from the admin endpoint', async () => {
    get.mockResolvedValueOnce({
      data: { configured: true, redirect_uri: 'http://localhost:18765' }
    })

    const capabilities = await geminiAppAPI.getCapabilities()

    expect(get).toHaveBeenCalledWith('/admin/gemini-app/oauth/capabilities')
    expect(capabilities).toEqual({ configured: true, redirect_uri: 'http://localhost:18765' })
  })

  it('posts the proxy id when generating an auth url', async () => {
    post.mockResolvedValueOnce({
      data: { auth_url: 'https://accounts.google.com/o/oauth2/v2/auth', session_id: 's1', state: 'st' }
    })

    const response = await geminiAppAPI.generateAuthUrl({ proxy_id: 12 })

    expect(post).toHaveBeenCalledWith('/admin/gemini-app/oauth/auth-url', { proxy_id: 12 })
    expect(response.session_id).toBe('s1')
  })

  it('omits the proxy id when none is configured', async () => {
    post.mockResolvedValueOnce({ data: { auth_url: 'u', session_id: 's', state: 'st' } })

    await geminiAppAPI.generateAuthUrl({})

    expect(post).toHaveBeenCalledWith('/admin/gemini-app/oauth/auth-url', {})
  })

  it('exchanges a pasted authorization code together with its state', async () => {
    post.mockResolvedValueOnce({
      data: { access_token: 'at', refresh_token: 'rt', expires_at: 1_900_000_000 }
    })

    const tokenInfo = await geminiAppAPI.exchangeCode({
      session_id: 'session-1',
      state: 'state-1',
      code: '4/0Aexample',
      proxy_id: 3
    })

    expect(post).toHaveBeenCalledWith('/admin/gemini-app/oauth/exchange-code', {
      session_id: 'session-1',
      state: 'state-1',
      code: '4/0Aexample',
      proxy_id: 3
    })
    expect(tokenInfo.refresh_token).toBe('rt')
  })

  it('sends the refresh token and optional proxy when validating an RT', async () => {
    post.mockResolvedValue({ data: { access_token: 'at' } })

    await geminiAppAPI.refreshGeminiAppToken('1//refresh', 4)
    await geminiAppAPI.refreshGeminiAppToken('1//refresh-no-proxy', null)

    expect(post).toHaveBeenNthCalledWith(1, '/admin/gemini-app/oauth/refresh-token', {
      refresh_token: '1//refresh',
      proxy_id: 4
    })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/gemini-app/oauth/refresh-token', {
      refresh_token: '1//refresh-no-proxy'
    })
  })
})
