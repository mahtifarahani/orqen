import type { CatalogProject } from '@/api/hooks/catalog'

/** A catalog row can be added when it is not configured yet and still exists upstream. */
export const selectable = (p: CatalogProject) => !p.project_id && !p.gone

const STALE_MS = 7 * 24 * 3600 * 1000

/** True when the catalog was never refreshed or the last refresh is older than a week. */
export const isStale = (refreshedAt: string | null | undefined) => !refreshedAt || Date.now() - new Date(refreshedAt).getTime() > STALE_MS
