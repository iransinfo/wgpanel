import { useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import {
  LayoutDashboard,
  Server,
  Users,
  KeyRound,
  ShieldCheck,
  ScrollText,
  LogOut,
  ShieldHalf,
  Settings,
  Menu,
  X,
  Sun,
  Moon,
  Monitor,
  BookOpen,
  ExternalLink,
} from 'lucide-react'
import { useAuth } from '../lib/auth'
import type { AdminRole } from '../lib/auth'
import { useTheme, type Theme } from '../lib/theme'

interface NavItem {
  to: string
  label: string
  icon: typeof LayoutDashboard
  minRole?: AdminRole
  // External items are plain links opened in a new tab (e.g. the static Swagger
  // page), not SPA routes - they render as <a>, never match as "active".
  external?: boolean
}

interface NavGroup {
  label: string | null
  items: NavItem[]
}

const roleRank: Record<AdminRole, number> = { support: 1, operator: 2, super_admin: 3 }

const navGroups: NavGroup[] = [
  {
    label: null,
    items: [{ to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard }],
  },
  {
    label: 'Infrastructure',
    items: [
      { to: '/nodes', label: 'Nodes', icon: Server },
      { to: '/accounts', label: 'Accounts', icon: Users },
    ],
  },
  {
    label: 'Administration',
    items: [
      { to: '/api-keys', label: 'API Keys', icon: KeyRound, minRole: 'super_admin' },
      { to: '/admins', label: 'Admin Users', icon: ShieldCheck, minRole: 'super_admin' },
      { to: '/audit-log', label: 'Audit Log', icon: ScrollText, minRole: 'super_admin' },
      { to: '/settings', label: 'Settings', icon: Settings, minRole: 'super_admin' },
      // No minRole: the Swagger page is a static asset nginx serves to anyone
      // with the URL anyway, so hiding the link would only be security theater.
      { to: '/api-docs.html', label: 'API Docs', icon: BookOpen, external: true },
    ],
  },
]

const themeOrder: Theme[] = ['light', 'dark', 'system']
const themeMeta: Record<Theme, { icon: typeof Sun; label: string }> = {
  light: { icon: Sun, label: 'Light' },
  dark: { icon: Moon, label: 'Dark' },
  system: { icon: Monitor, label: 'System' },
}

function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  return (
    <div className="flex items-center gap-1 rounded-lg bg-inset p-1" role="group" aria-label="Theme">
      {themeOrder.map((t) => {
        const Icon = themeMeta[t].icon
        const active = theme === t
        return (
          <button
            key={t}
            type="button"
            title={`${themeMeta[t].label} theme`}
            aria-pressed={active}
            onClick={() => setTheme(t)}
            className={`flex h-6.5 flex-1 cursor-pointer items-center justify-center rounded-md transition-colors ${
              active ? 'bg-surface text-fg shadow-xs ring-1 ring-edge' : 'text-faint hover:text-muted'
            }`}
          >
            <Icon className="h-3.5 w-3.5" />
          </button>
        )
      })}
    </div>
  )
}

export function DashboardLayout() {
  const { logout, user } = useAuth()
  const location = useLocation()
  const [mobileOpen, setMobileOpen] = useState(false)

  const visibleGroups = navGroups
    .map((group) => ({
      ...group,
      items: group.items.filter(
        (item) => !item.minRole || (user && roleRank[user.role] >= roleRank[item.minRole]),
      ),
    }))
    .filter((group) => group.items.length > 0)

  const initials = (user?.username ?? '?').slice(0, 2).toUpperCase()
  const currentLabel =
    navGroups.flatMap((g) => g.items).find((i) => location.pathname.startsWith(i.to))?.label ?? 'WGPanel'

  const sidebar = (
    <>
      <div className="flex items-center gap-2.5 px-5 py-5">
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-indigo-500 to-violet-600 text-white shadow-md shadow-indigo-600/30">
          <ShieldHalf className="h-5 w-5" />
        </div>
        <div className="leading-tight">
          <p className="text-[15px] font-semibold tracking-tight text-fg">WGPanel</p>
          <p className="text-[11px] font-medium text-faint">WireGuard fleet</p>
        </div>
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-2">
        {visibleGroups.map((group, gi) => (
          <div key={group.label ?? gi}>
            {group.label && (
              <p className="mb-1.5 px-3 text-[11px] font-semibold tracking-wider text-faint uppercase">
                {group.label}
              </p>
            )}
            <div className="space-y-0.5">
              {group.items.map((item) =>
                item.external ? (
                  <a
                    key={item.to}
                    href={item.to}
                    target="_blank"
                    rel="noreferrer"
                    onClick={() => setMobileOpen(false)}
                    className="group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium text-muted transition-colors hover:bg-inset hover:text-fg"
                  >
                    <item.icon className="h-4 w-4 shrink-0 text-faint group-hover:text-muted" />
                    {item.label}
                    <ExternalLink className="ml-auto h-3.5 w-3.5 shrink-0 text-faint opacity-0 transition-opacity group-hover:opacity-100" />
                  </a>
                ) : (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    onClick={() => setMobileOpen(false)}
                    className={({ isActive }) =>
                      `group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
                        isActive
                          ? 'bg-accent-soft text-accent-fg'
                          : 'text-muted hover:bg-inset hover:text-fg'
                      }`
                    }
                  >
                    {({ isActive }) => (
                      <>
                        {isActive && (
                          <span className="absolute top-1/2 left-0 h-4 w-1 -translate-y-1/2 rounded-r-full bg-accent" />
                        )}
                        <item.icon className={`h-4 w-4 shrink-0 ${isActive ? 'text-accent-fg' : 'text-faint group-hover:text-muted'}`} />
                        {item.label}
                      </>
                    )}
                  </NavLink>
                ),
              )}
            </div>
          </div>
        ))}
      </nav>

      <div className="space-y-3 border-t border-edge p-4">
        <div className="flex items-center gap-3 px-1">
          <div className="flex h-8.5 w-8.5 shrink-0 items-center justify-center rounded-full bg-accent-soft text-xs font-semibold text-accent-fg">
            {initials}
          </div>
          <div className="min-w-0 flex-1 leading-tight">
            <p className="truncate text-sm font-medium text-fg">{user?.username}</p>
            <p className="text-xs text-faint capitalize">{user?.role.replace('_', ' ')}</p>
          </div>
          <button
            onClick={logout}
            title="Log out"
            className="flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-lg text-faint transition-colors hover:bg-inset hover:text-fg"
          >
            <LogOut className="h-4 w-4" />
            <span className="sr-only">Log out</span>
          </button>
        </div>
        <ThemeToggle />
      </div>
    </>
  )

  return (
    <div className="flex min-h-screen bg-bg">
      {/* Desktop sidebar */}
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 flex-col border-r border-edge bg-surface lg:flex">
        {sidebar}
      </aside>

      {/* Mobile slide-over sidebar */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="animate-backdrop-in absolute inset-0 bg-slate-950/50 backdrop-blur-[2px]" onClick={() => setMobileOpen(false)} />
          <aside className="animate-dialog-in absolute inset-y-0 left-0 flex w-72 flex-col border-r border-edge bg-surface shadow-2xl">
            <button
              type="button"
              onClick={() => setMobileOpen(false)}
              aria-label="Close menu"
              className="absolute top-4 right-3 cursor-pointer rounded-lg p-1.5 text-faint hover:bg-inset hover:text-fg"
            >
              <X className="h-5 w-5" />
            </button>
            {sidebar}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mobile top bar */}
        <header className="sticky top-0 z-30 flex items-center gap-3 border-b border-edge bg-surface/90 px-4 py-3 backdrop-blur lg:hidden">
          <button
            type="button"
            onClick={() => setMobileOpen(true)}
            aria-label="Open menu"
            className="cursor-pointer rounded-lg p-1.5 text-muted hover:bg-inset hover:text-fg"
          >
            <Menu className="h-5 w-5" />
          </button>
          <span className="text-sm font-semibold tracking-tight text-fg">{currentLabel}</span>
        </header>

        <main className="flex-1 overflow-y-auto">
          <div className="mx-auto w-full max-w-7xl p-5 sm:p-8 lg:p-10">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
