import { apiClient } from '@/api/client'

export {
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_EN,
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZH,
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZHTW,
  ADMIN_COMPLIANCE_VERSION_FALLBACK,
} from '@/constants/adminCompliance'

export interface AdminComplianceAcknowledgement {
  version: string
  document_zh: string
  document_zh_tw?: string
  document_en: string
  language?: string
  admin_user_id: number
  ip_address?: string
  user_agent?: string
  accepted_at: string
}

export interface AdminComplianceStatus {
  required: boolean
  version: string
  document_path_zh: string
  document_path_zh_tw: string
  document_path_en: string
  document_url_zh: string
  document_url_zh_tw: string
  document_url_en: string
  ack_phrase_zh: string
  ack_phrase_zh_tw: string
  ack_phrase_en: string
  acknowledgement?: AdminComplianceAcknowledgement
}

export interface AcceptAdminComplianceRequest {
  phrase: string
  language: string
}

export const adminComplianceAPI = {
  async getStatus(): Promise<AdminComplianceStatus> {
    const { data } = await apiClient.get<AdminComplianceStatus>('/admin/compliance')
    return data
  },

  async accept(payload: AcceptAdminComplianceRequest): Promise<AdminComplianceStatus> {
    const { data } = await apiClient.post<AdminComplianceStatus>('/admin/compliance/accept', payload)
    return data
  },
}

export default adminComplianceAPI
