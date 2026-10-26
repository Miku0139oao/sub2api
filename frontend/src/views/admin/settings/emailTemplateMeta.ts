/**
 * Catalog keys for the email-template event picker.
 *
 * Kept in a plain TS module (not `<script setup>`) so tests can walk the map and
 * assert every key exists in en/zh/zh-TW.
 */

export interface EmailEventMetaKeys {
  labelKey: string
  timingKey: string
  categoryKey: string
}

const prefix = 'admin.settings.emailTemplates.events'

export const EVENT_META_KEYS: Record<string, EmailEventMetaKeys> = {
  'auth.verify_code': {
    labelKey: `${prefix}.authVerifyCode.label`,
    timingKey: `${prefix}.authVerifyCode.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.auth',
  },
  'auth.password_reset': {
    labelKey: `${prefix}.authPasswordReset.label`,
    timingKey: `${prefix}.authPasswordReset.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.auth',
  },
  'notification_email.verify_code': {
    labelKey: `${prefix}.notificationEmailVerifyCode.label`,
    timingKey: `${prefix}.notificationEmailVerifyCode.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.auth',
  },
  'subscription.purchase_success': {
    labelKey: `${prefix}.subscriptionPurchaseSuccess.label`,
    timingKey: `${prefix}.subscriptionPurchaseSuccess.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.subscription',
  },
  'subscription.expiry_reminder': {
    labelKey: `${prefix}.subscriptionExpiryReminder.label`,
    timingKey: `${prefix}.subscriptionExpiryReminder.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.subscription',
  },
  'balance.low': {
    labelKey: `${prefix}.balanceLow.label`,
    timingKey: `${prefix}.balanceLow.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.billing',
  },
  'balance.recharge_success': {
    labelKey: `${prefix}.balanceRechargeSuccess.label`,
    timingKey: `${prefix}.balanceRechargeSuccess.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.billing',
  },
  'account.quota_alert': {
    labelKey: `${prefix}.accountQuotaAlert.label`,
    timingKey: `${prefix}.accountQuotaAlert.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.admin',
  },
  'content_moderation.violation_notice': {
    labelKey: `${prefix}.contentModerationViolationNotice.label`,
    timingKey: `${prefix}.contentModerationViolationNotice.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.riskControl',
  },
  'content_moderation.account_disabled': {
    labelKey: `${prefix}.contentModerationAccountDisabled.label`,
    timingKey: `${prefix}.contentModerationAccountDisabled.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.riskControl',
  },
  'ops.alert': {
    labelKey: `${prefix}.opsAlert.label`,
    timingKey: `${prefix}.opsAlert.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.ops',
  },
  'ops.scheduled_report': {
    labelKey: `${prefix}.opsScheduledReport.label`,
    timingKey: `${prefix}.opsScheduledReport.timing`,
    categoryKey: 'admin.settings.emailTemplates.categories.ops',
  },
}

export const CATEGORY_KEYS: Record<string, string> = {
  auth: 'admin.settings.emailTemplates.categories.auth',
  subscription: 'admin.settings.emailTemplates.categories.subscription',
  billing: 'admin.settings.emailTemplates.categories.billing',
  admin: 'admin.settings.emailTemplates.categories.admin',
  risk_control: 'admin.settings.emailTemplates.categories.riskControl',
  ops: 'admin.settings.emailTemplates.categories.ops',
  notification: 'admin.settings.emailTemplates.categories.notification',
}

export const DEFAULT_CATEGORY_KEY = 'admin.settings.emailTemplates.categories.notification'
