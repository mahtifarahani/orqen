import type { CatherdRungDto } from '@/api/generated/model'

/** Whether Orqen lets a rung be added to a profile (D57): scored and offered, or listing unknown. */
export function rungAddable(r: CatherdRungDto): boolean {
  return r.scored && (r.availability === 'available' || r.availability === 'unknown_listing')
}

/** Text reason why a rung cannot be added, or null when it can. */
export function rungBlockedReason(r: CatherdRungDto, t: (k: string) => string): string | null {
  if (rungAddable(r)) return null
  if (!r.scored) return t('catalog.availability.unscoredNeedsMapping')
  return t(`catalog.availability.${r.availability}`)
}

