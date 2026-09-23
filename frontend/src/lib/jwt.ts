interface DecodedAccessToken {
  admin_id: string
  username: string
  role: string
  exp?: number
}

/** Decodes the payload of a JWT for display purposes only (e.g. showing the
 * signed-in username/role in the UI) - this is NOT verification, the server
 * is the sole authority on whether the token is valid. */
export function decodeAccessToken(token: string): DecodedAccessToken | null {
  try {
    const payload = token.split('.')[1]
    if (!payload) return null
    const json = atob(payload.replace(/-/g, '+').replace(/_/g, '/'))
    return JSON.parse(json) as DecodedAccessToken
  } catch {
    return null
  }
}
