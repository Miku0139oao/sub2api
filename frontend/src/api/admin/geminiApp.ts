/**
 * Admin Gemini App API endpoints
 * Handles Gemini App (Google OAuth flexible-api) login flows for administrators.
 */

import { apiClient } from '../client'

export interface GeminiAppCapabilities {
  configured: boolean
  redirect_uri?: string
  message?: string
}

export interface GeminiAppAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
  redirect_uri?: string
}

export interface GeminiAppAuthUrlRequest {
  proxy_id?: number
}

export interface GeminiAppExchangeCodeRequest {
  session_id: string
  state: string
  code: string
  proxy_id?: number
}

export interface GeminiAppTokenInfo {
  access_token?: string
  refresh_token?: string
  token_type?: string
  expires_at?: number | string
  expires_in?: number
  scope?: string
  email?: string
  subject?: string
  [key: string]: unknown
}

export async function getCapabilities(): Promise<GeminiAppCapabilities> {
  const { data } = await apiClient.get<GeminiAppCapabilities>('/admin/gemini-app/oauth/capabilities')
  return data
}

export async function generateAuthUrl(
  payload: GeminiAppAuthUrlRequest
): Promise<GeminiAppAuthUrlResponse> {
  const { data } = await apiClient.post<GeminiAppAuthUrlResponse>(
    '/admin/gemini-app/oauth/auth-url',
    payload
  )
  return data
}

export async function exchangeCode(
  payload: GeminiAppExchangeCodeRequest
): Promise<GeminiAppTokenInfo> {
  const { data } = await apiClient.post<GeminiAppTokenInfo>(
    '/admin/gemini-app/oauth/exchange-code',
    payload
  )
  return data
}

export async function refreshGeminiAppToken(
  refreshToken: string,
  proxyId?: number | null
): Promise<GeminiAppTokenInfo> {
  const payload: Record<string, any> = { refresh_token: refreshToken }
  if (proxyId) payload.proxy_id = proxyId

  const { data } = await apiClient.post<GeminiAppTokenInfo>(
    '/admin/gemini-app/oauth/refresh-token',
    payload
  )
  return data
}

export default { getCapabilities, generateAuthUrl, exchangeCode, refreshGeminiAppToken }
