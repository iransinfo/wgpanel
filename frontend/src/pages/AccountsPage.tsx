import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Users, Download, Ban, CheckCircle2, RotateCcw, Trash2, Server, Copy, FileDown, Link2, RefreshCw, MonitorSmartphone, Search, Pencil } from 'lucide-react'
import QRCode from 'qrcode'
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'
import { apiFetch, ApiError } from '../lib/api'
import { getAccessToken } from '../lib/tokenStore'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Select } from '../components/ui/Select'
import { Field } from '../components/ui/Field'
import { Dialog } from '../components/ui/Dialog'
import { ConfirmDialog } from '../components/ui/ConfirmDialog'
import { Badge, statusTone } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'
import { Progress } from '../components/ui/Progress'

interface Node {
  id: string
  name: string
  node_group: string
}

interface AccountPeer {
  node_id: string
  node_name: string
  assigned_ip: string
  online: boolean
  last_handshake_at: string | null
}

interface UsageSample {
  bucket: string
  rx_bytes: number
  tx_bytes: number
}

interface Account {
  id: string
  external_ref: string | null
  label: string
  public_key: string
  peers: AccountPeer[]
  data_quota_bytes: number | null
  data_used_bytes: number
  expiry_at: string | null
  device_limit: number | null
  device_limit_exceeded: boolean
  device_limit_hard_enforce: boolean
  bandwidth_limit_mbps: number | null
  subscription_path: string
  subscription_url: string | null
  status: string
  suspend_reason: string | null
  created_at: string
  updated_at: string
}

interface AccountDevice {
  id: string
  source_endpoint: string
  node_id: string
  node_name: string
  first_seen_at: string
  last_seen_at: string
  active: boolean
}

interface DevicesResponse {
  devices: AccountDevice[] | null
  active_devices: number
  device_limit: number | null
  device_limit_exceeded: boolean
}

type DetailTab = 'overview' | 'devices' | 'usage' | 'edit'

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

// Mirrors the backend's confFilename (subscription.go): WireGuard clients derive
// the tunnel name from the imported file's name, and Android/wg-quick tunnel names
// must be 1-15 chars of [a-zA-Z0-9_=+.-] - a raw account label (Unicode, spaces,
// or just too long) makes the app reject the file with "Invalid name".
function confFilename(label: string): string {
  let stem = label.toLowerCase().replace(/[^a-z0-9._-]+/g, '-')
  if (stem.length > 15) stem = stem.slice(0, 15)
  // Trim AFTER truncating so a cut landing on a separator doesn't leave a
  // trailing "-"/"." right before the extension.
  stem = stem.replace(/^[-.]+|[-.]+$/g, '')
  return `${stem === '' ? 'wgpanel' : stem}.conf`
}

// Mirrors the backend's peerOnlineWindow (180s) purely for display wording - the
// authoritative online/offline bit itself always comes from the server's `online`
// field, never recomputed here from last_handshake_at.
function formatLastSeen(lastHandshakeAt: string | null): string {
  if (!lastHandshakeAt) return 'never connected'
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(lastHandshakeAt).getTime()) / 1000))
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

export function AccountsPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()

  const [createOpen, setCreateOpen] = useState(false)
  const [detailAccount, setDetailAccount] = useState<Account | null>(null)
  const [detailInitialTab, setDetailInitialTab] = useState<DetailTab>('overview')
  const [deleteTarget, setDeleteTarget] = useState<Account | null>(null)
  const [search, setSearch] = useState('')

  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: () => apiFetch<{ accounts: Account[] | null }>('/api/v1/accounts'),
  })
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<{ default_data_quota_gb: number | null; default_device_limit: number | null }>('/api/v1/settings'),
  })

  const accounts = accountsQuery.data?.accounts ?? []
  const nodes = nodesQuery.data?.nodes ?? []

  const query = search.trim().toLowerCase()
  const filteredAccounts = query
    ? accounts.filter((a) => a.label.toLowerCase().includes(query) || a.external_ref?.toLowerCase().includes(query))
    : accounts

  function openDetail(account: Account, tab: DetailTab = 'overview') {
    setDetailInitialTab(tab)
    setDetailAccount(account)
  }

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['accounts'] })
  }

  const suspendMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}/suspend`, { method: 'POST', body: JSON.stringify({}) }),
    onSuccess: (account) => {
      push('success', `${account.label} suspended`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to suspend account'),
  })

  const enableMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}/enable`, { method: 'POST' }),
    onSuccess: (account) => {
      push('success', `${account.label} enabled`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to enable account'),
  })

  const renewMutation = useMutation({
    mutationFn: ({ id, extendDays }: { id: string; extendDays: number }) =>
      apiFetch<Account>(`/api/v1/accounts/${id}/renew`, {
        method: 'POST',
        body: JSON.stringify({ extend_days: extendDays }),
      }),
    onSuccess: (account) => {
      push('success', `${account.label} renewed`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to renew account'),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}`, { method: 'DELETE' }),
    onSuccess: (account) => {
      push('success', `${account.label} deleted`)
      invalidate()
      setDeleteTarget(null)
      setDetailAccount(null)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to delete account'),
  })

  const patchMutation = useMutation({
    mutationFn: ({ id, body }: { id: string; body: Record<string, unknown> }) =>
      apiFetch<Account>(`/api/v1/accounts/${id}`, { method: 'PATCH', body: JSON.stringify(body) }),
    onSuccess: (account) => {
      push('success', `${account.label} updated`)
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to update account'),
  })

  const rotateSubscriptionMutation = useMutation({
    mutationFn: (id: string) => apiFetch<Account>(`/api/v1/accounts/${id}/subscription/rotate`, { method: 'POST' }),
    onSuccess: (account) => {
      push('success', 'Subscription URL rotated - the old link no longer works')
      invalidate()
      setDetailAccount(account)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to rotate subscription URL'),
  })

  return (
    <div>
      <PageHeader
        title="Accounts"
        description="WireGuard peer accounts - each one gets a peer on every eligible node, not just one."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" />
            New account
          </Button>
        }
      />

      {accounts.length > 0 && (
        <div className="relative mb-4 max-w-xs">
          <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-faint" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search accounts…"
            className="pl-9"
            aria-label="Search accounts"
          />
        </div>
      )}

      <Card className="overflow-hidden">
        {accountsQuery.isLoading && <TableSkeleton cols={5} />}
        {accountsQuery.isError && (
          <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load accounts.</p>
        )}
        {!accountsQuery.isLoading && !accountsQuery.isError && accounts.length === 0 && (
          <EmptyState
            icon={Users}
            title="No accounts yet"
            description="Create the first WireGuard account to get started."
            action={
              <Button variant="secondary" onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" />
                New account
              </Button>
            }
          />
        )}
        {!accountsQuery.isLoading && !accountsQuery.isError && accounts.length > 0 && filteredAccounts.length === 0 && (
          <EmptyState icon={Search} title="No matching accounts" description={`Nothing matches "${search.trim()}".`} />
        )}
        {!accountsQuery.isLoading && !accountsQuery.isError && filteredAccounts.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Label</Th>
                <Th>Nodes</Th>
                <Th>Connection</Th>
                <Th>Usage</Th>
                <Th>Status</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {filteredAccounts.map((a) => {
                const onlineCount = a.peers.filter((p) => p.online).length
                const mostRecentHandshake = a.peers.reduce<string | null>((latest, p) => {
                  if (!p.last_handshake_at) return latest
                  if (!latest || new Date(p.last_handshake_at) > new Date(latest)) return p.last_handshake_at
                  return latest
                }, null)
                return (
                  <Tr key={a.id} interactive onClick={() => openDetail(a)}>
                    <Td className="font-medium text-fg">{a.label}</Td>
                    <Td className="text-muted">
                      <span className="inline-flex items-center gap-1.5">
                        <Server className="h-3.5 w-3.5 text-faint" />
                        {a.peers.length === 0 ? 'none yet' : `${a.peers.length} node${a.peers.length === 1 ? '' : 's'}`}
                      </span>
                    </Td>
                    <Td className="text-muted">
                      {a.peers.length === 0 ? (
                        '—'
                      ) : (
                        <span className="inline-flex items-center gap-1.5">
                          <span className={`h-2 w-2 rounded-full ${onlineCount > 0 ? 'bg-emerald-500' : 'bg-edge-strong'}`} />
                          {onlineCount > 0
                            ? `Online on ${onlineCount}/${a.peers.length} nodes`
                            : `Offline · ${formatLastSeen(mostRecentHandshake)}`}
                        </span>
                      )}
                    </Td>
                    <Td className="text-muted">
                      <span className="tabular-nums">
                        {formatBytes(a.data_used_bytes)}
                        {a.data_quota_bytes ? ` / ${formatBytes(a.data_quota_bytes)}` : ''}
                      </span>
                      {a.data_quota_bytes != null && a.data_quota_bytes > 0 && (
                        <Progress value={a.data_used_bytes} max={a.data_quota_bytes} className="mt-1.5 max-w-32" />
                      )}
                    </Td>
                    <Td>
                      <span className="inline-flex flex-wrap items-center gap-1">
                        <Badge dot tone={statusTone(a.status)}>{a.status}</Badge>
                        {a.device_limit_exceeded && <Badge tone="amber">over device limit</Badge>}
                      </span>
                    </Td>
                    <Td>
                      <div className="flex justify-end">
                        <Button
                          variant="ghost"
                          size="icon"
                          title="Edit account"
                          aria-label={`Edit ${a.label}`}
                          onClick={(e) => {
                            e.stopPropagation()
                            openDetail(a, 'edit')
                          }}
                        >
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </Td>
                  </Tr>
                )
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <CreateAccountDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        nodes={nodes}
        defaultQuotaGb={settingsQuery.data?.default_data_quota_gb ?? null}
        defaultDeviceLimit={settingsQuery.data?.default_device_limit ?? null}
        onCreated={() => {
          invalidate()
          setCreateOpen(false)
        }}
      />

      {detailAccount && (
        <AccountDetailDialog
          account={detailAccount}
          initialTab={detailInitialTab}
          onClose={() => setDetailAccount(null)}
          onSuspend={() => suspendMutation.mutate(detailAccount.id)}
          onEnable={() => enableMutation.mutate(detailAccount.id)}
          onRenew={(days) => renewMutation.mutate({ id: detailAccount.id, extendDays: days })}
          onDelete={() => setDeleteTarget(detailAccount)}
          onPatch={(body) => patchMutation.mutate({ id: detailAccount.id, body })}
          onRotateSubscription={() => rotateSubscriptionMutation.mutate(detailAccount.id)}
          busy={
            suspendMutation.isPending ||
            enableMutation.isPending ||
            renewMutation.isPending ||
            patchMutation.isPending ||
            rotateSubscriptionMutation.isPending
          }
        />
      )}

      {deleteTarget && (
        <ConfirmDialog
          open={!!deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => deleteMutation.mutate(deleteTarget.id)}
          title="Delete account"
          description={`This will permanently delete "${deleteTarget.label}" and release its assigned IPs. This cannot be undone.`}
          confirmLabel="Delete"
          danger
          submitting={deleteMutation.isPending}
        />
      )}
    </div>
  )
}

function CreateAccountDialog({
  open,
  onClose,
  nodes,
  defaultQuotaGb,
  defaultDeviceLimit,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  nodes: Node[]
  defaultQuotaGb: number | null
  defaultDeviceLimit: number | null
  onCreated: () => void
}) {
  const { push } = useToast()
  const [label, setLabel] = useState('')
  const [nodeId, setNodeId] = useState('')
  const [quotaGb, setQuotaGb] = useState('')
  const [deviceLimit, setDeviceLimit] = useState('')
  const [bandwidthMbps, setBandwidthMbps] = useState('')
  const [duration, setDuration] = useState('none')
  const [customExpiry, setCustomExpiry] = useState('')
  const [error, setError] = useState<string | null>(null)

  // The dialog stays mounted (just hidden) between opens, so plain useState
  // initializers only ever see the settings-derived defaults from the very first
  // render, before that fetch has necessarily resolved. Re-sync on every open instead.
  useEffect(() => {
    if (!open) return
    setLabel('')
    setNodeId('')
    setQuotaGb(defaultQuotaGb?.toString() ?? '')
    setDeviceLimit(defaultDeviceLimit?.toString() ?? '')
    setBandwidthMbps('')
    setDuration('none')
    setCustomExpiry('')
    setError(null)
  }, [open, defaultQuotaGb, defaultDeviceLimit])

  function resolveExpiryAt(): string | undefined {
    if (duration === 'none') return undefined
    if (duration === 'custom') {
      return customExpiry ? new Date(`${customExpiry}T23:59:59`).toISOString() : undefined
    }
    const days = Number(duration)
    return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString()
  }

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/accounts', {
        method: 'POST',
        body: JSON.stringify({
          label,
          node_id: nodeId || undefined,
          data_quota_gb: quotaGb ? Number(quotaGb) : undefined,
          device_limit: deviceLimit ? Number(deviceLimit) : undefined,
          bandwidth_limit_mbps: bandwidthMbps ? Number(bandwidthMbps) : undefined,
          expiry_at: resolveExpiryAt(),
        }),
      }),
    onSuccess: () => {
      push('success', `Account "${label}" created`)
      setLabel('')
      setNodeId('')
      setQuotaGb('')
      setDeviceLimit('')
      setBandwidthMbps('')
      setDuration('none')
      setCustomExpiry('')
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create account'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New account">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Label">
          <Input value={label} onChange={(e) => setLabel(e.target.value)} required placeholder="e.g. alice-laptop" />
        </Field>
        <Field
          label="Nodes"
          hint="By default this account gets a peer on every registered node, and stays synced as nodes are added later."
        >
          <Select value={nodeId} onChange={(e) => setNodeId(e.target.value)}>
            <option value="">All eligible nodes (recommended)</option>
            {nodes.map((n) => (
              <option key={n.id} value={n.id}>
                Pin to just: {n.name} ({n.node_group})
              </option>
            ))}
          </Select>
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Data quota (GB)">
            <Input type="number" min="0" step="0.1" value={quotaGb} onChange={(e) => setQuotaGb(e.target.value)} placeholder="Unlimited" />
          </Field>
          <Field label="Device limit">
            <Input type="number" min="1" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder="Unlimited" />
          </Field>
          <Field
            label="Bandwidth (Mbps)"
            hint="Rate limit enforced on every node this account connects through."
          >
            <Input type="number" min="1" value={bandwidthMbps} onChange={(e) => setBandwidthMbps(e.target.value)} placeholder="Unlimited" />
          </Field>
        </div>
        <Field label="Expires">
          <Select value={duration} onChange={(e) => setDuration(e.target.value)}>
            <option value="none">Never</option>
            <option value="7">In 7 days</option>
            <option value="30">In 30 days</option>
            <option value="90">In 90 days</option>
            <option value="180">In 180 days</option>
            <option value="365">In 1 year</option>
            <option value="custom">Custom date…</option>
          </Select>
          {duration === 'custom' && (
            <Input
              type="date"
              className="mt-2"
              value={customExpiry}
              onChange={(e) => setCustomExpiry(e.target.value)}
              min={new Date().toISOString().slice(0, 10)}
              required
            />
          )}
        </Field>
        {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create account'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

function SectionTitle({ children }: { children: ReactNode }) {
  return <p className="mb-3 text-xs font-semibold tracking-wider text-faint uppercase">{children}</p>
}

function toDateInputValue(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function AccountDetailDialog({
  account,
  initialTab,
  onClose,
  onSuspend,
  onEnable,
  onRenew,
  onDelete,
  onPatch,
  onRotateSubscription,
  busy,
}: {
  account: Account
  initialTab: DetailTab
  onClose: () => void
  onSuspend: () => void
  onEnable: () => void
  onRenew: (days: number) => void
  onDelete: () => void
  onPatch: (body: Record<string, unknown>) => void
  onRotateSubscription: () => void
  busy: boolean
}) {
  const { push } = useToast()
  const [tab, setTab] = useState<DetailTab>(initialTab)
  const [configText, setConfigText] = useState<string | null>(null)
  const [configNodeName, setConfigNodeName] = useState<string | null>(null)
  const [qrDataUrl, setQrDataUrl] = useState<string | null>(null)
  const [configLoading, setConfigLoading] = useState<string | null>(null)

  // Prefer the separate subscription origin (Settings -> Subscription domain &
  // port) when one is configured; otherwise the links live on the panel's origin.
  const subscriptionUrl = account.subscription_url ?? `${window.location.origin}${account.subscription_path}`

  const usageQuery = useQuery({
    queryKey: ['account-usage', account.id],
    queryFn: () => apiFetch<{ bucket: string; samples: UsageSample[] | null }>(`/api/v1/accounts/${account.id}/usage?bucket=hour`),
  })

  const devicesQuery = useQuery({
    queryKey: ['account-devices', account.id],
    queryFn: () => apiFetch<DevicesResponse>(`/api/v1/accounts/${account.id}/devices`),
  })
  const devices = devicesQuery.data?.devices ?? []
  const usageSamples = usageQuery.data?.samples ?? []
  const chartData = usageSamples.map((s) => ({
    time: new Date(s.bucket).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' }),
    'Downloaded (RX)': s.rx_bytes,
    'Uploaded (TX)': s.tx_bytes,
  }))

  const tabs: { id: DetailTab; label: string }[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'devices', label: devicesQuery.data ? `Devices (${devicesQuery.data.active_devices})` : 'Devices' },
    { id: 'usage', label: 'Usage' },
    { id: 'edit', label: 'Edit' },
  ]

  async function loadConfig(nodeId: string, nodeName: string) {
    setConfigLoading(nodeId)
    setQrDataUrl(null)
    setConfigNodeName(nodeName)
    try {
      const res = await fetch(`/api/v1/accounts/${account.id}/config?node_id=${nodeId}`, {
        headers: { Authorization: `Bearer ${getAccessToken() ?? ''}` },
      })
      if (!res.ok) {
        // Surface the real reason (e.g. "the node this account is on has no public
        // key set yet") instead of a generic message - a node without a reported
        // WireGuard public key (agent never got a real wg interface up) is a common,
        // specific, actionable case, not a mystery failure.
        let message = 'Failed to load config'
        try {
          const body = (await res.json()) as { error?: { message?: string } }
          message = body?.error?.message ?? message
        } catch {
          // not JSON - fall back to the generic message
        }
        throw new Error(message)
      }
      const text = await res.text()
      setConfigText(text)
      // Generated entirely client-side - this config contains the account's real
      // private key, so it's never sent anywhere but rendered locally as a QR
      // code for scanning into a mobile WireGuard app.
      setQrDataUrl(await QRCode.toDataURL(text, { width: 240, margin: 1 }))
    } catch (err) {
      push('error', err instanceof Error ? err.message : 'Could not load WireGuard config')
    } finally {
      setConfigLoading(null)
    }
  }

  return (
    <Dialog open onClose={onClose} title={account.label} maxWidthClassName="max-w-3xl">
      {/* Status strip - always visible regardless of tab */}
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Badge dot tone={statusTone(account.status)}>{account.status}</Badge>
        {account.device_limit_exceeded && <Badge tone="amber">over device limit</Badge>}
        {account.suspend_reason && <span className="text-xs text-muted">reason: {account.suspend_reason}</span>}
        <span className="ml-auto text-xs text-faint">created {new Date(account.created_at).toLocaleDateString()}</span>
      </div>

      {/* Tab bar */}
      <div className="mb-5 flex gap-1 border-b border-edge" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={`-mb-px cursor-pointer border-b-2 px-3 py-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:ring-accent/50 focus-visible:outline-none ${
              tab === t.id
                ? 'border-accent text-accent-fg'
                : 'border-transparent text-muted hover:border-edge-strong hover:text-fg'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'overview' && (
        <div>
          <div className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2">
            <Row label="Usage">
              <span className="tabular-nums">
                {formatBytes(account.data_used_bytes)}
                {account.data_quota_bytes ? ` / ${formatBytes(account.data_quota_bytes)}` : ' / unlimited'}
              </span>
            </Row>
            <Row label="Expires">{account.expiry_at ? new Date(account.expiry_at).toLocaleString() : 'Never'}</Row>
            <Row label="Device limit">{account.device_limit ?? 'Unlimited'}</Row>
            <Row label="Bandwidth limit">
              {account.bandwidth_limit_mbps != null ? `${account.bandwidth_limit_mbps} Mbps` : 'Unlimited'}
            </Row>
          </div>
          {account.data_quota_bytes != null && account.data_quota_bytes > 0 && (
            <Progress value={account.data_used_bytes} max={account.data_quota_bytes} className="mt-3" />
          )}
          <p className="mt-3 flex items-baseline justify-between gap-4 text-sm">
            <span className="shrink-0 text-muted">Public key</span>
            <span className="min-w-0 text-right font-mono text-xs break-all text-fg">{account.public_key}</span>
          </p>

          <div className="mt-5 border-t border-edge pt-5">
            <SectionTitle>Subscription URL</SectionTitle>
            <div className="flex items-center gap-2">
              <code className="h-9 min-w-0 flex-1 truncate rounded-lg border border-edge bg-inset px-3 py-2 font-mono text-xs leading-5 text-fg">
                {subscriptionUrl}
              </code>
              <Button
                variant="secondary"
                size="icon"
                title="Copy subscription URL"
                onClick={() => {
                  navigator.clipboard.writeText(subscriptionUrl)
                  push('success', 'Subscription URL copied')
                }}
              >
                <Copy className="h-3.5 w-3.5" />
              </Button>
              <Button variant="secondary" size="icon" onClick={onRotateSubscription} disabled={busy} title="Rotate (invalidates the current URL)">
                <RefreshCw className="h-3.5 w-3.5" />
              </Button>
            </div>
            <p className="mt-2 text-xs leading-relaxed text-muted">
              <Link2 className="mr-1 inline h-3 w-3" />
              Always serves this account's latest config, picking the best node automatically (append{' '}
              <code>?region=</code> to prefer a region, or <code>/nodes</code> to list choices). Rotate if the link leaks —
              the old URL stops working immediately.
            </p>
          </div>

          <div className="mt-5 border-t border-edge pt-5">
            <SectionTitle>Node peers ({account.peers.length})</SectionTitle>
            {account.peers.length === 0 ? (
              <p className="text-sm text-muted">No node peers yet - no eligible node was available when this account was created.</p>
            ) : (
              <div className="space-y-2">
                {account.peers.map((p) => (
                  <div key={p.node_id} className="flex items-center justify-between gap-3 rounded-lg border border-edge px-3 py-2.5">
                    <div className="min-w-0">
                      <p className="flex items-center gap-1.5 text-sm font-medium text-fg">
                        <span
                          className={`h-2 w-2 shrink-0 rounded-full ${p.online ? 'bg-emerald-500' : 'bg-edge-strong'}`}
                          title={p.online ? 'Online' : 'Offline'}
                        />
                        <span className="truncate">{p.node_name}</span>
                        <span className={`shrink-0 text-xs font-normal ${p.online ? 'text-emerald-600 dark:text-emerald-400' : 'text-faint'}`}>
                          {p.online ? 'online' : `offline · ${formatLastSeen(p.last_handshake_at)}`}
                        </span>
                      </p>
                      <p className="mt-0.5 font-mono text-xs text-muted">{p.assigned_ip}</p>
                    </div>
                    <Button variant="secondary" size="sm" onClick={() => loadConfig(p.node_id, p.node_name)} disabled={configLoading === p.node_id}>
                      <Download className="h-3.5 w-3.5" />
                      {configLoading === p.node_id ? 'Loading…' : 'Config'}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>

          {configText !== null && (
            <div className="mt-5 space-y-3 border-t border-edge pt-5">
              <SectionTitle>WireGuard config{configNodeName ? ` — ${configNodeName}` : ''}</SectionTitle>
              <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
                {qrDataUrl && (
                  <img
                    src={qrDataUrl}
                    alt="WireGuard config QR code - scan with the WireGuard mobile app"
                    className="h-40 w-40 shrink-0 rounded-lg border border-edge bg-white p-2"
                  />
                )}
                <pre className="max-h-64 min-w-0 flex-1 overflow-auto rounded-lg border border-edge bg-inset p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-fg">
                  {configText}
                </pre>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="secondary"
                  onClick={() => {
                    navigator.clipboard.writeText(configText)
                    push('success', 'Config copied to clipboard')
                  }}
                >
                  <Copy className="h-4 w-4" />
                  Copy config
                </Button>
                <Button
                  variant="secondary"
                  onClick={() => {
                    // application/octet-stream, NOT text/plain: Android Chrome renames
                    // text/plain downloads whose extension it doesn't associate with
                    // that type, so "x.conf" lands as "x.conf.txt" - which the WireGuard
                    // app then refuses to import.
                    const blob = new Blob([configText], { type: 'application/octet-stream' })
                    const url = URL.createObjectURL(blob)
                    const link = document.createElement('a')
                    link.href = url
                    link.download = confFilename(`${account.label}-${configNodeName ?? 'node'}`)
                    link.click()
                    URL.revokeObjectURL(url)
                  }}
                >
                  <FileDown className="h-4 w-4" />
                  Download .conf
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {tab === 'devices' && (
        <div>
          <SectionTitle>
            Devices
            {devicesQuery.data && (
              <span className="ml-2 font-normal normal-case tracking-normal">
                ({devicesQuery.data.active_devices} active
                {account.device_limit != null ? ` / limit ${account.device_limit}` : ''})
              </span>
            )}
          </SectionTitle>
          {devicesQuery.isLoading && <p className="text-sm text-muted">Loading…</p>}
          {!devicesQuery.isLoading && devices.length === 0 && (
            <p className="text-sm text-muted">
              <MonitorSmartphone className="mr-1 inline h-3.5 w-3.5" />
              No devices observed yet — client endpoints appear here once they connect.
            </p>
          )}
          {devices.length > 0 && (
            <div className="max-h-72 space-y-1.5 overflow-y-auto">
              {devices.map((d) => (
                <div key={d.id} className="flex items-center justify-between gap-3 rounded-lg border border-edge px-3 py-2 text-xs">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className={`h-2 w-2 shrink-0 rounded-full ${d.active ? 'bg-emerald-500' : 'bg-edge-strong'}`} />
                    <span className="truncate font-mono text-fg">{d.source_endpoint}</span>
                  </span>
                  <span className="shrink-0 text-muted">
                    via {d.node_name} · {d.active ? 'active now' : formatLastSeen(d.last_seen_at)}
                  </span>
                </div>
              ))}
            </div>
          )}
          <label className="mt-4 flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={account.device_limit_hard_enforce}
              disabled={busy}
              onChange={(e) => onPatch({ device_limit_hard_enforce: e.target.checked })}
            />
            <span>
              <span className="font-medium">Auto-suspend when over the device limit</span>
              <p className="mt-0.5 text-xs leading-relaxed text-muted">
                Off = detection only (the account is flagged but keeps working). NAT and roaming can look like extra
                devices, so enable this only when you're sure.
              </p>
            </span>
          </label>
        </div>
      )}

      {tab === 'usage' && (
        <div>
          <SectionTitle>Usage (last 7 days)</SectionTitle>
          {usageQuery.isLoading && <p className="text-sm text-muted">Loading…</p>}
          {!usageQuery.isLoading && chartData.length === 0 && (
            <p className="text-sm text-muted">No traffic recorded yet.</p>
          )}
          {chartData.length > 0 && (
            <div className="h-64 w-full">
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={chartData} margin={{ top: 4, right: 8, left: 8, bottom: 0 }}>
                  <defs>
                    <linearGradient id="rxGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#6366f1" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#6366f1" stopOpacity={0} />
                    </linearGradient>
                    <linearGradient id="txGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#10b981" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#10b981" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="var(--ui-edge)" />
                  <XAxis dataKey="time" tick={{ fontSize: 10, fill: 'var(--ui-faint)' }} stroke="var(--ui-edge)" minTickGap={30} />
                  <YAxis tick={{ fontSize: 10, fill: 'var(--ui-faint)' }} stroke="var(--ui-edge)" tickFormatter={(v) => formatBytes(v)} width={60} />
                  <Tooltip
                    formatter={(v) => formatBytes(Number(v ?? 0))}
                    contentStyle={{
                      fontSize: 12,
                      backgroundColor: 'var(--ui-surface)',
                      border: '1px solid var(--ui-edge)',
                      borderRadius: 8,
                      color: 'var(--ui-fg)',
                    }}
                  />
                  <Area type="monotone" dataKey="Downloaded (RX)" stroke="#6366f1" fill="url(#rxGradient)" strokeWidth={1.5} />
                  <Area type="monotone" dataKey="Uploaded (TX)" stroke="#10b981" fill="url(#txGradient)" strokeWidth={1.5} />
                </AreaChart>
              </ResponsiveContainer>
            </div>
          )}
        </div>
      )}

      {tab === 'edit' && <EditAccountForm account={account} busy={busy} onPatch={onPatch} />}

      {/* Actions - always visible regardless of tab */}
      <div className="mt-6 flex flex-wrap gap-2 border-t border-edge pt-4">
        <Button variant="secondary" onClick={() => onRenew(30)} disabled={busy}>
          <RotateCcw className="h-4 w-4" />
          Renew 30d
        </Button>
        {account.status === 'suspended' ? (
          <Button variant="secondary" onClick={onEnable} disabled={busy}>
            <CheckCircle2 className="h-4 w-4" />
            Enable
          </Button>
        ) : (
          <Button variant="secondary" onClick={onSuspend} disabled={busy}>
            <Ban className="h-4 w-4" />
            Suspend
          </Button>
        )}
        <Button variant="danger" onClick={onDelete} disabled={busy} className="ml-auto">
          <Trash2 className="h-4 w-4" />
          Delete
        </Button>
      </div>
    </Dialog>
  )
}

function EditAccountForm({
  account,
  busy,
  onPatch,
}: {
  account: Account
  busy: boolean
  onPatch: (body: Record<string, unknown>) => void
}) {
  // Initial form values derived from the account - recomputed when the server
  // confirms an update (updated_at changes) so the form resyncs to saved state.
  const initial = useMemo(
    () => ({
      label: account.label,
      quotaGb: account.data_quota_bytes != null ? String(Number((account.data_quota_bytes / 1024 ** 3).toFixed(2))) : '',
      deviceLimit: account.device_limit?.toString() ?? '',
      bandwidth: account.bandwidth_limit_mbps?.toString() ?? '',
      expiry: account.expiry_at ? toDateInputValue(account.expiry_at) : '',
    }),
    [account.id, account.updated_at], // eslint-disable-line react-hooks/exhaustive-deps
  )

  const [label, setLabel] = useState(initial.label)
  const [quotaGb, setQuotaGb] = useState(initial.quotaGb)
  const [deviceLimit, setDeviceLimit] = useState(initial.deviceLimit)
  const [bandwidth, setBandwidth] = useState(initial.bandwidth)
  const [expiry, setExpiry] = useState(initial.expiry)

  useEffect(() => {
    setLabel(initial.label)
    setQuotaGb(initial.quotaGb)
    setDeviceLimit(initial.deviceLimit)
    setBandwidth(initial.bandwidth)
    setExpiry(initial.expiry)
  }, [initial])

  // Only changed fields are sent - the PATCH endpoint treats omitted fields as
  // "leave unchanged". Quota/device-limit/expiry cannot be cleared back to
  // unlimited/never by the API (COALESCE semantics), so an emptied field is
  // treated as "no change". Bandwidth is the exception: 0 removes the limit.
  const changes: Record<string, unknown> = {}
  if (label.trim() && label.trim() !== initial.label) changes.label = label.trim()
  if (quotaGb !== initial.quotaGb && quotaGb !== '' && Number(quotaGb) > 0) changes.data_quota_gb = Number(quotaGb)
  if (deviceLimit !== initial.deviceLimit && deviceLimit !== '' && Number(deviceLimit) >= 1) changes.device_limit = Number(deviceLimit)
  if (bandwidth !== initial.bandwidth) changes.bandwidth_limit_mbps = bandwidth === '' ? 0 : Number(bandwidth)
  if (expiry !== initial.expiry && expiry !== '') changes.expiry_at = new Date(`${expiry}T23:59:59`).toISOString()
  const hasChanges = Object.keys(changes).length > 0

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!hasChanges) return
    onPatch(changes)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <Field label="Label">
        <Input value={label} onChange={(e) => setLabel(e.target.value)} required />
      </Field>
      <div className="grid grid-cols-2 gap-4">
        <Field
          label="Data quota (GB)"
          hint={account.data_quota_bytes != null ? 'Raising or lowering replaces the quota. Cannot be cleared back to unlimited.' : 'Currently unlimited - set a number to add a quota.'}
        >
          <Input type="number" min="0.1" step="0.1" value={quotaGb} onChange={(e) => setQuotaGb(e.target.value)} placeholder={account.data_quota_bytes == null ? 'Unlimited' : undefined} />
        </Field>
        <Field
          label="Device limit"
          hint={account.device_limit != null ? 'Cannot be cleared back to unlimited.' : 'Currently unlimited - set a number to add a limit.'}
        >
          <Input type="number" min="1" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder={account.device_limit == null ? 'Unlimited' : undefined} />
        </Field>
        <Field label="Bandwidth (Mbps)" hint="Blank removes the limit (unshaped).">
          <Input type="number" min="0" value={bandwidth} onChange={(e) => setBandwidth(e.target.value)} placeholder="Unlimited" />
        </Field>
        <Field
          label="Expires"
          hint={account.expiry_at ? 'Pick a new date to move the expiry. Cannot be cleared back to never - use Renew to extend.' : 'Currently never expires - pick a date to add an expiry.'}
        >
          <Input type="date" value={expiry} onChange={(e) => setExpiry(e.target.value)} min={new Date().toISOString().slice(0, 10)} />
        </Field>
      </div>
      <div className="flex items-center justify-end gap-3 border-t border-edge pt-4">
        {hasChanges && <span className="text-xs text-muted">{Object.keys(changes).length} change{Object.keys(changes).length === 1 ? '' : 's'} pending</span>}
        <Button type="submit" disabled={busy || !hasChanges}>
          {busy ? 'Saving…' : 'Save changes'}
        </Button>
      </div>
    </form>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <span className="shrink-0 text-muted">{label}</span>
      <span className="min-w-0 text-right text-fg">{children}</span>
    </div>
  )
}
