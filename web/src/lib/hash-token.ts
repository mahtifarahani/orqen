let hashToken: string | undefined

/** Reads `#token=…` once and removes the fragment so the token does not linger in the address bar. */
export function takeHashToken(): string {
  if (hashToken !== undefined) return hashToken
  const params = new URLSearchParams(window.location.hash.replace(/^#/, ''))
  hashToken = params.get('token') ?? ''
  if (window.location.hash) {
    window.history.replaceState(null, '', window.location.pathname + window.location.search)
  }
  return hashToken
}

/** Test hook: forget the cached token. */
export function resetHashToken() {
  hashToken = undefined
}
