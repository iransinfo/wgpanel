import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, KeyRound, Ban, RotateCw, Copy, AlertTriangle } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'

interface ApiKey {
  id: string
  key_id: string
  label: string
  node_groups: string[]
  permissions: string[]
  revoked: boolean
  created_at: string
}

interface ApiKeyCreated extends ApiKey {
  secret: string
}

const ALL_PERMISSIONS = ['read', 'create', 'update', 'suspend', 'delete']

export function ApiKeysPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()
  const [createOpen, setCreateOpen] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<ApiKey | null>(null)
  const [revealedSecret, setRevealedSecret] = useState<{ keyId: string; secret: string } | null>(null)

  const keysQuery = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => apiFetch<{ api_keys: ApiKey[] | null }>('/api/v1/api-keys'),
  })
  const keys = keysQuery.data?.api_keys ?? []

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['api-keys'] })
  }

  const revokeMutation = useMutation({
    mutationFn: (id: string) => apiFetch(`/api/v1/api-keys/${id}/revoke`, { method: 'POST' }),
    onSuccess: () => {
      push('success', 'API key revoked')
      invalidate()
      setRevokeTarget(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to revoke key'),
  })

  const rotateMutation = useMutation({
    mutationFn: (id: string) => apiFetch<ApiKeyCreated>(`/api/v1/api-keys/${id}/rotate`, { method: 'POST' }),
    onSuccess: (key) => {
      push('success', `Secret rotated for ${key.label}`)
      invalidate()
      setRevealedSecret({ keyId: key.key_id, secret: key.secret })
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to rotate key'),
  })

  return (
    <div>
      <PageHeader
        title="API Keys"
        description="Credentials for the Telegram sales bot and other integrations to call this panel's API."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" />
            New API key
          </Button>
        }
      />

      <Card className="overflow-hidden">
        {keysQuery.isLoading && <TableSkeleton cols={5} />}
        {keysQuery.isError && <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load API keys.</p>}
        {!keysQuery.isLoading && !keysQuery.isError && keys.length === 0 && (
          <EmptyState
            icon={KeyRound}
            title="No API keys yet"
            description="Create one to let your Telegram bot talk to this panel."
            action={
              <Button variant="secondary" onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" />
                New API key
              </Button>
            }
          />
        )}
        {!keysQuery.isLoading && !keysQuery.isError && keys.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Label</Th>
                <Th>Key ID</Th>
                <Th>Permissions</Th>
                <Th>Node groups</Th>
                <Th>Status</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {keys.map((k) => (
                <Tr key={k.id}>
                  <Td className="font-medium text-fg">{k.label}</Td>
                  <Td className="font-mono text-xs text-muted">{k.key_id}</Td>
                  <Td>
                    <div className="flex flex-wrap gap-1">
                      {k.permissions.map((p) => (
                        <Badge key={p} tone="blue">
                          {p}
                        </Badge>
                      ))}
                    </div>
                  </Td>
                  <Td className="text-muted">{k.node_groups.length > 0 ? k.node_groups.join(', ') : 'all'}</Td>
                  <Td>
                    <Badge dot tone={k.revoked ? 'red' : 'green'}>{k.revoked ? 'revoked' : 'active'}</Badge>
                  </Td>
                  <Td>
                    {!k.revoked && (
                      <div className="flex justify-end gap-1.5">
                        <Button variant="secondary" size="sm" onClick={() => rotateMutation.mutate(k.id)} disabled={rotateMutation.isPending}>
                          <RotateCw className="h-3.5 w-3.5" />
                          Rotate
                        </Button>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setRevokeTarget(k)}
                          className="text-rose-600 hover:border-rose-300 hover:bg-rose-500/10 dark:text-rose-400 dark:hover:border-rose-500/40"
                        >
                          <Ban className="h-3.5 w-3.5" />
                          Revoke
                        </Button>
                      </div>
                    )}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <CreateApiKeyDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(created) => {
          invalidate()
          setCreateOpen(false)
          setRevealedSecret({ keyId: created.key_id, secret: created.secret })
        }}
      />

      {revokeTarget && (
        <ConfirmDialog
          open={!!revokeTarget}
          onClose={() => setRevokeTarget(null)}
          onConfirm={() => revokeMutation.mutate(revokeTarget.id)}
          title="Revoke API key"
          description={`"${revokeTarget.label}" will immediately stop being able to authenticate. This cannot be undone.`}
          confirmLabel="Revoke"
          danger
          submitting={revokeMutation.isPending}
        />
      )}

      {revealedSecret && (
        <Dialog open onClose={() => setRevealedSecret(null)} title="Secret key">
          <div className="mb-4 flex items-start gap-2.5 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2.5 text-sm text-amber-700 dark:text-amber-400">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <span>This secret is shown once. Store it securely — it cannot be retrieved again.</span>
          </div>
          <div className="space-y-3">
            <div>
              <p className="mb-1.5 text-xs font-medium text-muted">Key ID</p>
              <p className="rounded-lg border border-edge bg-inset p-2.5 font-mono text-xs text-fg">
                {revealedSecret.keyId}
              </p>
            </div>
            <div>
              <p className="mb-1.5 text-xs font-medium text-muted">Secret</p>
              <p className="break-all rounded-lg border border-edge bg-inset p-2.5 font-mono text-xs leading-relaxed text-fg">
                {revealedSecret.secret}
              </p>
            </div>
          </div>
          <div className="mt-4 flex justify-end">
            <Button
              variant="secondary"
              onClick={() => {
                navigator.clipboard.writeText(revealedSecret.secret)
                push('success', 'Secret copied to clipboard')
              }}
            >
              <Copy className="h-4 w-4" />
              Copy secret
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  )
}

function CreateApiKeyDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: (created: ApiKeyCreated) => void
}) {
  const [label, setLabel] = useState('')
  const [nodeGroups, setNodeGroups] = useState('')
  const [permissions, setPermissions] = useState<string[]>(['read'])
  const [error, setError] = useState<string | null>(null)

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch<ApiKeyCreated>('/api/v1/api-keys', {
        method: 'POST',
        body: JSON.stringify({
          label,
          node_groups: nodeGroups
            .split(',')
            .map((g) => g.trim())
            .filter(Boolean),
          permissions,
        }),
      }),
    onSuccess: (created) => {
      setLabel('')
      setNodeGroups('')
      setPermissions(['read'])
      onCreated(created)
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create API key'),
  })

  function togglePermission(perm: string) {
    setPermissions((prev) => (prev.includes(perm) ? prev.filter((p) => p !== perm) : [...prev, perm]))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New API key">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Label">
          <Input value={label} onChange={(e) => setLabel(e.target.value)} required placeholder="e.g. telegram-sales-bot" />
        </Field>
        <Field label="Node groups" labelSuffix="(comma-separated, blank = all)">
          <Input value={nodeGroups} onChange={(e) => setNodeGroups(e.target.value)} placeholder="eu-west, us-east" />
        </Field>
        <div>
          <p className="mb-2 block text-sm font-medium text-fg">Permissions</p>
          <div className="flex flex-wrap gap-2">
            {ALL_PERMISSIONS.map((perm) => (
              <button
                type="button"
                key={perm}
                onClick={() => togglePermission(perm)}
                aria-pressed={permissions.includes(perm)}
                className={`cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-accent/50 focus-visible:outline-none ${
                  permissions.includes(perm)
                    ? 'border-accent bg-accent text-white'
                    : 'border-edge-strong/70 text-muted hover:border-edge-strong hover:text-fg'
                }`}
              >
                {perm}
              </button>
            ))}
          </div>
        </div>
        {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending || permissions.length === 0}>
            {createMutation.isPending ? 'Creating…' : 'Create key'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
