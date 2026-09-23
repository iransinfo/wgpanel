import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { useAuth } from '../lib/auth'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Select } from '../components/ui/Select'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'

interface AdminUser {
  id: string
  username: string
  role: string
  created_at: string
}

const ROLE_TONE: Record<string, 'blue' | 'green' | 'slate'> = {
  super_admin: 'blue',
  operator: 'green',
  support: 'slate',
}

export function AdminUsersPage() {
  const queryClient = useQueryClient()
  const { user: currentUser } = useAuth()
  const { push } = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [editTarget, setEditTarget] = useState<AdminUser | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null)

  const adminsQuery = useQuery({
    queryKey: ['admin-users'],
    queryFn: () => apiFetch<{ admins: AdminUser[] | null }>('/api/v1/admins'),
  })
  const admins = adminsQuery.data?.admins ?? []

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['admin-users'] })
  }

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<void>(`/api/v1/admins/${id}`, { method: 'DELETE' }),
    onSuccess: () => {
      push('success', `${deleteTarget?.username} deleted`)
      invalidate()
      setDeleteTarget(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to delete admin'),
  })

  return (
    <div>
      <PageHeader
        title="Admin Users"
        description="Panel operators with dashboard access. Role controls what they can change."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" />
            New admin
          </Button>
        }
      />

      <Card className="overflow-hidden">
        {adminsQuery.isLoading && <TableSkeleton cols={3} />}
        {adminsQuery.isError && <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load admin users.</p>}
        {!adminsQuery.isLoading && !adminsQuery.isError && admins.length === 0 && (
          <EmptyState icon={ShieldCheck} title="No admin users found" />
        )}
        {!adminsQuery.isLoading && !adminsQuery.isError && admins.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Username</Th>
                <Th>Role</Th>
                <Th>Created</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {admins.map((a) => {
                const isSelf = currentUser?.username === a.username
                return (
                  <Tr key={a.id} interactive onClick={() => setEditTarget(a)}>
                    <Td className="font-medium text-fg">
                      {a.username}
                      {isSelf && <span className="ml-2 text-xs font-normal text-faint">(you)</span>}
                    </Td>
                    <Td>
                      <Badge tone={ROLE_TONE[a.role] ?? 'slate'}>{a.role.replace('_', ' ')}</Badge>
                    </Td>
                    <Td className="text-muted">{new Date(a.created_at).toLocaleDateString()}</Td>
                    <Td>
                      {!isSelf && (
                        <div className="flex justify-end">
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={(e) => {
                              e.stopPropagation()
                              setDeleteTarget(a)
                            }}
                            className="text-rose-600 hover:border-rose-300 hover:bg-rose-500/10 dark:text-rose-400 dark:hover:border-rose-500/40"
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            Delete
                          </Button>
                        </div>
                      )}
                    </Td>
                  </Tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <CreateAdminDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          invalidate()
          setCreateOpen(false)
        }}
      />

      {editTarget && (
        <EditAdminDialog
          admin={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={() => {
            invalidate()
            setEditTarget(null)
          }}
        />
      )}

      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete admin"
          description={`This will permanently remove "${deleteTarget.username}"'s access to the panel. This cannot be undone.`}
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
    </div>
  )
}

function EditAdminDialog({
  admin,
  onClose,
  onSaved,
}: {
  admin: AdminUser
  onClose: () => void
  onSaved: () => void
}) {
  const { push } = useToast()
  const [role, setRole] = useState(admin.role)
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setRole(admin.role)
    setPassword('')
    setError(null)
  }, [admin])

  const saveMutation = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {}
      if (role !== admin.role) body.role = role
      if (password) body.password = password
      return apiFetch(`/api/v1/admins/${admin.id}`, { method: 'PATCH', body: JSON.stringify(body) })
    },
    onSuccess: () => {
      push('success', `${admin.username} updated`)
      onSaved()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to update admin'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    if (role === admin.role && !password) {
      onClose()
      return
    }
    saveMutation.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={`Edit ${admin.username}`}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Role">
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="support">Support (read-only)</option>
            <option value="operator">Operator</option>
            <option value="super_admin">Super admin</option>
          </Select>
        </Field>
        <Field label="Reset password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Leave blank to keep current password"
            autoComplete="new-password"
          />
        </Field>
        {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={saveMutation.isPending}>
            {saveMutation.isPending ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function CreateAdminDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const { push } = useToast()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('support')
  const [error, setError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/admins', {
        method: 'POST',
        body: JSON.stringify({ username, password, role }),
      }),
    onSuccess: () => {
      push('success', `Admin "${username}" created`)
      setUsername('')
      setPassword('')
      setRole('support')
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create admin'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New admin user">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Username">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} required autoComplete="off" />
        </Field>
        <Field label="Password">
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="new-password"
          />
        </Field>
        <Field label="Role">
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="support">Support (read-only)</option>
            <option value="operator">Operator</option>
            <option value="super_admin">Super admin</option>
          </Select>
        </Field>
        {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create admin'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
