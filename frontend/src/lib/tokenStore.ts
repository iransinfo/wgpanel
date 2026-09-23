// The access token lives in memory only (never localStorage) - it's short-lived
// (15 min, see backend AccessTokenTTL) and this keeps it out of reach of anything
// that can read localStorage (e.g. an XSS payload persisting past a reload). The
// refresh token is longer-lived and stored in localStorage by lib/api.ts so a
// session can survive a page reload via a silent refresh on load.
let accessToken: string | null = null

export function getAccessToken(): string | null {
  return accessToken
}

export function setAccessToken(token: string | null): void {
  accessToken = token
}
