import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from './lib/auth'
import { ToastProvider } from './lib/toast'
import { ProtectedRoute } from './components/ProtectedRoute'
import { LoginPage } from './pages/LoginPage'
import { DashboardLayout } from './pages/DashboardLayout'
import { DashboardPage } from './pages/DashboardPage'
import { NodesPage } from './pages/NodesPage'
import { AccountsPage } from './pages/AccountsPage'
import { ApiKeysPage } from './pages/ApiKeysPage'
import { AdminUsersPage } from './pages/AdminUsersPage'
import { AuditLogPage } from './pages/AuditLogPage'
import { SettingsPage } from './pages/SettingsPage'

const queryClient = new QueryClient()

function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <ToastProvider>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route
                element={
                  <ProtectedRoute>
                    <DashboardLayout />
                  </ProtectedRoute>
                }
              >
                <Route path="/dashboard" element={<DashboardPage />} />
                <Route path="/nodes" element={<NodesPage />} />
                <Route path="/accounts" element={<AccountsPage />} />
                <Route path="/api-keys" element={<ApiKeysPage />} />
                <Route path="/admins" element={<AdminUsersPage />} />
                <Route path="/audit-log" element={<AuditLogPage />} />
                <Route path="/settings" element={<SettingsPage />} />
              </Route>
              <Route path="*" element={<Navigate to="/dashboard" replace />} />
            </Routes>
          </ToastProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  )
}

export default App
