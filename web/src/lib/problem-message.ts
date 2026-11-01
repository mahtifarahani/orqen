import type { TFunction } from 'i18next'
import { ApiProblem } from '@/api/problem'

/** Human message for an error: translated by problem `code` when known, else the server detail. */
export function problemMessage(t: TFunction, error: unknown): string {
  if (error instanceof ApiProblem) {
    const key = `problems.${error.code}`
    if (t(key, { defaultValue: '' }) !== '') return t(key)
    return error.detail || t('common.unexpectedError')
  }
  if (error instanceof TypeError) return t('common.networkError')
  return t('common.unexpectedError')
}
