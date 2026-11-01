const KEY = 'orqen.lastOrg'

export function getLastOrg(): string | null {
  try {
    return localStorage.getItem(KEY)
  } catch {
    return null
  }
}

export function setLastOrg(orgId: string) {
  try {
    localStorage.setItem(KEY, orgId)
  } catch {
    /* ignore */
  }
}
