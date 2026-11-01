import type { TFunction } from 'i18next'

/**
 * Text for a readiness blocker or warning code. Codes may carry an argument after a colon
 * (`backend_key_missing:grok`); the colon would be read as an i18next namespace, so it is split off and passed as `{{arg}}`.
 */
export function readinessText(t: TFunction, kind: 'blockers' | 'warnings', code: string, detail?: string): string {
  const [base = code, arg = ''] = code.split(':', 2)
  return t(`readiness.${kind}.${base}`, { defaultValue: detail || code, arg })
}
