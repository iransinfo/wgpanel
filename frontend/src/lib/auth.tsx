import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { apiFetch, getStoredRefreshToken, setStoredRefreshToken } from './api'
import { setAccessToken } from './tokenStore'
import { decodeAccessToken } from './jwt'

export type AdminRole = 'super_admin' | 'operator' | 'support'

interface CurrentUser {
  username: string
  role: AdminRole
}

interface AuthContextValue {
  isAuthenticated: boolean
  isLoading: boolean
  user: CurrentUser | null
  login: (username: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined)

interface LoginResponse {
  access_token: string
  refresh_token: string
  expires_in_seconds: number
}

interface RefreshResponse {
  access_token: string
  expires_in_seconds: number
}

function userFromToken(token: string): CurrentUser | null {
  const decoded = decodeAccessToken(token)
  if (!decoded) return null
  return { username: decoded.username, role: decoded.role as AdminRole }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [user, setUser] = useState<CurrentUser | null>(null)

  useEffect(() => {
    // The access token is in-memory only, so a page reload always starts without
    // one - if a refresh token survived in localStorage, exchange it silently
    // instead of forcing a full re-login on every reload.
    const refreshToken = getStoredRefreshToken()
    if (!refreshToken) {
      setIsLoading(false)
      return
    }
    apiFetch<RefreshResponse>('/api/v1/auth/refresh', {
      method: 'POST',
      body: JSON.stringify({ refresh_token: refreshToken }),
    })
      .then((data) => {
        setAccessToken(data.access_token)
        setUser(userFromToken(data.access_token))
        setIsAuthenticated(true)
      })
      .catch(() => {
        setStoredRefreshToken(null)
      })
      .finally(() => setIsLoading(false))
  }, [])

  async function login(username: string, password: string) {
    const data = await apiFetch<LoginResponse>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    })
    setAccessToken(data.access_token)
    setStoredRefreshToken(data.refresh_token)
    setUser(userFromToken(data.access_token))
    setIsAuthenticated(true)
  }

  function logout() {
    setAccessToken(null)
    setStoredRefreshToken(null)
    setUser(null)
    setIsAuthenticated(false)
  }

  return (
    <AuthContext.Provider value={{ isAuthenticated, isLoading, user, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
