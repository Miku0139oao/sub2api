import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import adminComplianceAPI, { type AdminComplianceStatus } from '@/api/admin/compliance'
import {
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_EN,
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZH,
  ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZHTW,
  ADMIN_COMPLIANCE_VERSION_FALLBACK,
} from '@/constants/adminCompliance'
import { getLocale } from '@/i18n'
import { isTraditionalChineseLocale } from '@/i18n/locale'

const FALLBACK_DOCUMENT_PATH_ZH = 'docs/legal/admin-compliance.zh.md'
const FALLBACK_DOCUMENT_PATH_ZHTW = 'docs/legal/admin-compliance.zh-TW.md'
const FALLBACK_DOCUMENT_PATH_EN = 'docs/legal/admin-compliance.en.md'
const FALLBACK_DOCUMENT_URL_ZH = 'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.zh.md'
const FALLBACK_DOCUMENT_URL_ZHTW = 'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.zh-TW.md'
const FALLBACK_DOCUMENT_URL_EN = 'https://github.com/Wei-Shaw/sub2api/blob/main/docs/legal/admin-compliance.en.md'

export const useAdminComplianceStore = defineStore('adminCompliance', () => {
  const status = ref<AdminComplianceStatus | null>(null)
  const loading = ref(false)
  const submitting = ref(false)
  const initialized = ref(false)
  const forceVisible = ref(false)
  let stateVersion = 0

  const required = computed(() => status.value?.required === true)
  const shouldShow = computed(() => required.value || forceVisible.value)
  const currentLocale = computed(() => getLocale())
  const expectedPhrase = computed(() => {
    if (isTraditionalChineseLocale(currentLocale.value)) {
      return status.value?.ack_phrase_zh_tw || ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZHTW
    }
    if (currentLocale.value === 'zh') {
      return status.value?.ack_phrase_zh || ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZH
    }
    return status.value?.ack_phrase_en || ADMIN_COMPLIANCE_PHRASE_FALLBACK_EN
  })

  async function fetchStatus(): Promise<AdminComplianceStatus> {
    const version = stateVersion
    loading.value = true
    try {
      const nextStatus = await adminComplianceAPI.getStatus()
      if (version !== stateVersion) return nextStatus
      status.value = nextStatus
      initialized.value = true
      forceVisible.value = nextStatus.required
      return nextStatus
    } finally {
      if (version === stateVersion) loading.value = false
    }
  }

  async function accept(phrase: string): Promise<AdminComplianceStatus> {
    const version = stateVersion
    submitting.value = true
    try {
      const nextStatus = await adminComplianceAPI.accept({
        phrase,
        language: currentLocale.value,
      })
      if (version !== stateVersion) return nextStatus
      status.value = nextStatus
      forceVisible.value = nextStatus.required
      return nextStatus
    } finally {
      if (version === stateVersion) submitting.value = false
    }
  }

  function requireAcknowledgement(partialStatus?: Partial<AdminComplianceStatus>): void {
    status.value = {
      required: true,
      version: partialStatus?.version || status.value?.version || ADMIN_COMPLIANCE_VERSION_FALLBACK,
      document_path_zh:
        partialStatus?.document_path_zh || status.value?.document_path_zh || FALLBACK_DOCUMENT_PATH_ZH,
      document_path_zh_tw:
        partialStatus?.document_path_zh_tw || status.value?.document_path_zh_tw || FALLBACK_DOCUMENT_PATH_ZHTW,
      document_path_en:
        partialStatus?.document_path_en || status.value?.document_path_en || FALLBACK_DOCUMENT_PATH_EN,
      document_url_zh:
        partialStatus?.document_url_zh || status.value?.document_url_zh || FALLBACK_DOCUMENT_URL_ZH,
      document_url_zh_tw:
        partialStatus?.document_url_zh_tw || status.value?.document_url_zh_tw || FALLBACK_DOCUMENT_URL_ZHTW,
      document_url_en:
        partialStatus?.document_url_en || status.value?.document_url_en || FALLBACK_DOCUMENT_URL_EN,
      ack_phrase_zh:
        partialStatus?.ack_phrase_zh || status.value?.ack_phrase_zh || ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZH,
      ack_phrase_zh_tw:
        partialStatus?.ack_phrase_zh_tw || status.value?.ack_phrase_zh_tw || ADMIN_COMPLIANCE_PHRASE_FALLBACK_ZHTW,
      ack_phrase_en:
        partialStatus?.ack_phrase_en || status.value?.ack_phrase_en || ADMIN_COMPLIANCE_PHRASE_FALLBACK_EN,
      acknowledgement: status.value?.acknowledgement,
    }
    initialized.value = true
    forceVisible.value = true
  }

  function reset(): void {
    stateVersion++
    status.value = null
    loading.value = false
    submitting.value = false
    initialized.value = false
    forceVisible.value = false
  }

  return {
    status,
    loading,
    submitting,
    initialized,
    required,
    shouldShow,
    expectedPhrase,
    fetchStatus,
    accept,
    requireAcknowledgement,
    reset,
  }
})
