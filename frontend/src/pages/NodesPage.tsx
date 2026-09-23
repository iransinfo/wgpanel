import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Server, KeyRound, Copy, Activity, Pencil } from 'lucide-react'
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts'
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
import { Badge, statusTone } from '../components/ui/Badge'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'

interface Node {
  id: string
  name: string
  node_group: string
  region: string
  public_endpoint: string
  wg_subnet: string
  status: string
  capacity_max_peers: number
  public_key: string | null
  created_at: string
}

interface JoinToken {
  token: string
  expires_at: string | null
  unlimited: boolean
  // The exact host:port to give install-node.sh as the control-plane address
  // (panel domain + NODE_AGENT_PORT) - null if no panel domain is configured.
  panel_addr: string | null
}

interface MetricsSample {
  bucket: string
  cpu_percent: number | null
  mem_used_bytes: number | null
  mem_total_bytes: number | null
}

function formatBytesShort(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

interface SubnetGroup {
  label: string
  subnets: string[]
}

// Curated WireGuard peer-subnet choices offered when creating a node. Chosen to avoid
// the ranges consumer routers hand out by default (192.168.0.0/24, 192.168.1.0/24,
// 10.0.0.0/24, 10.0.1.0/24) - a client whose home LAN overlaps the tunnel subnet loses
// either its own LAN or the tunnel. 100.64.0.0/10 (RFC 6598 carrier-grade-NAT space) is
// the safest: almost never present on customer LANs. Each /24 holds ~253 peers (ipalloc
// reserves the network, broadcast, and .1 gateway); /23 and /22 are offered for
// high-capacity nodes. "Custom" keeps any valid CIDR possible.
const SUBNET_GROUPS: SubnetGroup[] = [
  {
    label: '10.x — recommended',
    subnets: ['10.66.0.0/24', '10.67.0.0/24', '10.68.0.0/24', '10.69.0.0/24', '10.70.0.0/24', '10.71.0.0/24'],
  },
  {
    label: '100.64/10 — carrier-grade NAT (safest vs. home LANs)',
    subnets: ['100.64.0.0/24', '100.65.0.0/24', '100.66.0.0/24', '100.100.0.0/24'],
  },
  {
    label: '172.16–31 — private',
    subnets: ['172.16.0.0/24', '172.20.0.0/24', '172.28.0.0/24', '172.31.0.0/24'],
  },
  {
    label: 'Larger nodes',
    subnets: ['10.80.0.0/23', '10.90.0.0/22', '100.72.0.0/23', '100.80.0.0/22'],
  },
]

const ALL_RECOMMENDED_SUBNETS = SUBNET_GROUPS.flatMap((g) => g.subnets)

// Usable peer count for a CIDR - mirrors ipalloc.NextFree's reservations (network,
// broadcast, and the .1 gateway) so labels match what the node can actually allocate.
function subnetPeerCapacity(cidr: string): number {
  const prefix = Number(cidr.split('/')[1])
  if (!Number.isFinite(prefix) || prefix < 0 || prefix > 32) return 0
  return Math.max(0, 2 ** (32 - prefix) - 3)
}

export function NodesPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()
  const { user } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [joinTokenNode, setJoinTokenNode] = useState<Node | null>(null)
  const [joinToken, setJoinToken] = useState<JoinToken | null>(null)
  const [detailNode, setDetailNode] = useState<Node | null>(null)
  const [editNode, setEditNode] = useState<Node | null>(null)

  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const nodes = nodesQuery.data?.nodes ?? []

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<{ default_node_capacity: number }>('/api/v1/settings'),
  })

  const joinTokenMutation = useMutation({
    mutationFn: ({ nodeId, unlimited }: { nodeId: string; unlimited: boolean }) =>
      apiFetch<JoinToken>(`/api/v1/nodes/${nodeId}/join-token`, {
        method: 'POST',
        body: JSON.stringify({ unlimited }),
      }),
    onSuccess: (data) => setJoinToken(data),
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to generate join token'),
  })

  return (
    <div>
      <PageHeader
        title="Nodes"
        description="WireGuard servers this panel provisions accounts onto."
        action={
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" />
            New node
          </Button>
        }
      />

      <Card className="overflow-hidden">
        {nodesQuery.isLoading && <TableSkeleton cols={5} />}
        {nodesQuery.isError && <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load nodes.</p>}
        {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 && (
          <EmptyState
            icon={Server}
            title="No nodes registered yet"
            description="Add a node, then run install-node.sh with its join token."
            action={
              <Button variant="secondary" onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" />
                New node
              </Button>
            }
          />
        )}
        {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th>Name</Th>
                <Th>Group</Th>
                <Th>Region</Th>
                <Th>Endpoint</Th>
                <Th>Status</Th>
                <Th>Capacity</Th>
                <Th className="text-right">Actions</Th>
              </tr>
            </THead>
            <tbody>
              {nodes.map((node) => (
                <Tr key={node.id} interactive onClick={() => setDetailNode(node)}>
                  <Td className="font-medium text-fg">{node.name}</Td>
                  <Td className="text-muted">{node.node_group}</Td>
                  <Td className="text-muted">{node.region ? <Badge tone="blue">{node.region}</Badge> : '—'}</Td>
                  <Td className="font-mono text-xs text-muted">{node.public_endpoint}</Td>
                  <Td>
                    <Badge dot tone={statusTone(node.status)}>{node.status}</Badge>
                  </Td>
                  <Td className="text-muted tabular-nums">{node.capacity_max_peers}</Td>
                  <Td>
                    <div className="flex justify-end gap-1.5">
                      <Button variant="secondary" size="sm" onClick={(e) => { e.stopPropagation(); setDetailNode(node) }}>
                        <Activity className="h-3.5 w-3.5" />
                        Metrics
                      </Button>
                      <Button variant="secondary" size="sm" onClick={(e) => { e.stopPropagation(); setEditNode(node) }}>
                        <Pencil className="h-3.5 w-3.5" />
                        Edit
                      </Button>
                      {user?.role === 'super_admin' && (
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setJoinToken(null)
                            setJoinTokenNode(node)
                          }}
                        >
                          <KeyRound className="h-3.5 w-3.5" />
                          Join token
                        </Button>
                      )}
                    </div>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      <CreateNodeDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        defaultCapacity={settingsQuery.data?.default_node_capacity ?? 250}
        usedSubnets={nodes.map((n) => n.wg_subnet)}
        onCreated={() => {
          queryClient.invalidateQueries({ queryKey: ['nodes'] })
          setCreateOpen(false)
        }}
      />

      {joinTokenNode && (
        <JoinTokenDialog
          node={joinTokenNode}
          token={joinToken}
          onGenerate={(unlimited) => joinTokenMutation.mutate({ nodeId: joinTokenNode.id, unlimited })}
          generating={joinTokenMutation.isPending}
          onClose={() => {
            setJoinTokenNode(null)
            setJoinToken(null)
          }}
        />
      )}

      {detailNode && <NodeDetailDialog node={detailNode} onClose={() => setDetailNode(null)} />}

      {editNode && (
        <EditNodeDialog
          node={editNode}
          onClose={() => setEditNode(null)}
          onSaved={() => {
            queryClient.invalidateQueries({ queryKey: ['nodes'] })
            setEditNode(null)
          }}
        />
      )}
    </div>
  )
}

function JoinTokenDialog({
  node,
  token,
  onGenerate,
  generating,
  onClose,
}: {
  node: Node
  token: JoinToken | null
  onGenerate: (unlimited: boolean) => void
  generating: boolean
  onClose: () => void
}) {
  const { push } = useToast()
  const [unlimited, setUnlimited] = useState(false)

  return (
    <Dialog open onClose={onClose} title={`Join token for ${node.name}`}>
      {!token && (
        <div className="space-y-4">
          <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border border-edge p-3 text-sm text-fg transition-colors hover:bg-inset/50">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={unlimited}
              onChange={(e) => setUnlimited(e.target.checked)}
            />
            <span>
              <span className="font-medium">Unlimited (reusable, never expires)</span>
              <p className="mt-1 text-xs leading-relaxed text-muted">
                Use this only to re-register this node's agent (rebuilt server, replaced hardware) - a normal token
                is single-use and expires, and works for onboarding a brand-new node. An unlimited token keeps
                working forever, so treat it like a long-lived credential.
              </p>
            </span>
          </label>
          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button onClick={() => onGenerate(unlimited)} disabled={generating}>
              <KeyRound className="h-4 w-4" />
              {generating ? 'Generating…' : 'Generate token'}
            </Button>
          </div>
        </div>
      )}
      {token && (
        <div className="space-y-3">
          <p className="text-sm leading-relaxed text-muted">
            Run <code className="rounded bg-inset px-1.5 py-0.5 font-mono text-xs text-fg">install-node.sh</code> on
            the server with this token.{' '}
            {token.unlimited
              ? 'This token is unlimited - it never expires and can be reused.'
              : `It expires at ${new Date(token.expires_at!).toLocaleString()}.`}
          </p>
          <p className="break-all rounded-lg border border-edge bg-inset p-3 font-mono text-xs leading-relaxed text-fg">
            {token.token}
          </p>
          <Button
            variant="secondary"
            onClick={() => {
              navigator.clipboard.writeText(token.token)
              push('success', 'Join token copied')
            }}
          >
            <Copy className="h-4 w-4" />
            Copy token
          </Button>
          {token.panel_addr && (
            <div className="border-t border-edge pt-3">
              <p className="text-sm leading-relaxed text-muted">
                When the installer asks for the <span className="font-medium text-fg">control plane address</span>,
                enter exactly this — it is the panel's node-agent port, <em>not</em> the web UI address:
              </p>
              <div className="mt-2 flex items-center gap-2">
                <code className="min-w-0 flex-1 truncate rounded-lg border border-edge bg-inset px-3 py-2 font-mono text-xs leading-5 text-fg">
                  {token.panel_addr}
                </code>
                <Button
                  variant="secondary"
                  size="icon"
                  title="Copy control plane address"
                  onClick={() => {
                    navigator.clipboard.writeText(token.panel_addr!)
                    push('success', 'Control plane address copied')
                  }}
                >
                  <Copy className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </Dialog>
  )
}

function EditNodeDialog({
  node,
  onClose,
  onSaved,
}: {
  node: Node
  onClose: () => void
  onSaved: () => void
}) {
  const { push } = useToast()
  const [name, setName] = useState(node.name)
  const [nodeGroup, setNodeGroup] = useState(node.node_group)
  const [region, setRegion] = useState(node.region)
  const [publicEndpoint, setPublicEndpoint] = useState(node.public_endpoint)
  const [capacity, setCapacity] = useState(node.capacity_max_peers.toString())
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setName(node.name)
    setNodeGroup(node.node_group)
    setRegion(node.region)
    setPublicEndpoint(node.public_endpoint)
    setCapacity(node.capacity_max_peers.toString())
    setError(null)
  }, [node])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch(`/api/v1/nodes/${node.id}`, {
        method: 'PATCH',
        body: JSON.stringify({
          name,
          node_group: nodeGroup,
          region,
          public_endpoint: publicEndpoint,
          capacity_max_peers: Number(capacity),
        }),
      }),
    onSuccess: () => {
      push('success', `${name} updated`)
      onSaved()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to update node'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    saveMutation.mutate()
  }

  return (
    <Dialog open onClose={onClose} title={`Edit ${node.name}`}>
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Node group">
            <Input value={nodeGroup} onChange={(e) => setNodeGroup(e.target.value)} placeholder="default" />
          </Field>
          <Field label="Capacity (peers)">
            <Input type="number" min="1" value={capacity} onChange={(e) => setCapacity(e.target.value)} required />
          </Field>
          <Field
            label="Region"
            hint="Optional steering label - clients asking for this region are preferred onto this node."
          >
            <Input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="e.g. eu, us-east" />
          </Field>
        </div>
        <Field label="Public endpoint">
          <Input value={publicEndpoint} onChange={(e) => setPublicEndpoint(e.target.value)} required placeholder="vpn1.example.com:51820" />
        </Field>
        <Field label="WireGuard subnet" hint="Not editable once peers may already be allocated from it.">
          <Input value={node.wg_subnet} disabled />
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

function NodeDetailDialog({ node, onClose }: { node: Node; onClose: () => void }) {
  const metricsQuery = useQuery({
    queryKey: ['node-metrics', node.id],
    queryFn: () => apiFetch<{ samples: MetricsSample[] | null }>(`/api/v1/nodes/${node.id}/metrics`),
  })
  const samples = metricsQuery.data?.samples ?? []
  const chartData = samples.map((s) => ({
    time: new Date(s.bucket).toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' }),
    'CPU %': s.cpu_percent !== null ? Number(s.cpu_percent.toFixed(1)) : null,
    'RAM %': s.mem_total_bytes ? Number(((100 * (s.mem_used_bytes ?? 0)) / s.mem_total_bytes).toFixed(1)) : null,
  }))
  const latest = samples[samples.length - 1]

  return (
    <Dialog open onClose={onClose} title={`${node.name} — health`} maxWidthClassName="max-w-2xl">
      <div className="space-y-3 text-sm">
        <div className="flex items-center justify-between gap-4">
          <span className="text-muted">Status</span>
          <Badge dot tone={statusTone(node.status)}>{node.status}</Badge>
        </div>
        {latest?.mem_total_bytes != null && latest.mem_used_bytes != null && (
          <div className="flex items-center justify-between gap-4">
            <span className="text-muted">Memory</span>
            <span className="text-fg tabular-nums">
              {formatBytesShort(latest.mem_used_bytes)} / {formatBytesShort(latest.mem_total_bytes)}
            </span>
          </div>
        )}
      </div>

      <div className="mt-4 border-t border-edge pt-4">
        <p className="mb-3 text-xs font-semibold tracking-wider text-faint uppercase">
          CPU &amp; RAM (last 24h)
        </p>
        {metricsQuery.isLoading && <p className="text-sm text-muted">Loading…</p>}
        {!metricsQuery.isLoading && chartData.length === 0 && (
          <p className="text-sm text-muted">No metrics reported yet - the node agent reports health every ~40s.</p>
        )}
        {chartData.length > 0 && (
          <div className="h-56 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartData} margin={{ top: 4, right: 8, left: 8, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--ui-edge)" />
                <XAxis dataKey="time" tick={{ fontSize: 10, fill: 'var(--ui-faint)' }} stroke="var(--ui-edge)" minTickGap={30} />
                <YAxis tick={{ fontSize: 10, fill: 'var(--ui-faint)' }} stroke="var(--ui-edge)" domain={[0, 100]} unit="%" width={40} />
                <Tooltip
                  contentStyle={{
                    fontSize: 12,
                    backgroundColor: 'var(--ui-surface)',
                    border: '1px solid var(--ui-edge)',
                    borderRadius: 8,
                    color: 'var(--ui-fg)',
                  }}
                />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                <Line type="monotone" dataKey="CPU %" stroke="#6366f1" dot={false} strokeWidth={1.5} connectNulls />
                <Line type="monotone" dataKey="RAM %" stroke="#f59e0b" dot={false} strokeWidth={1.5} connectNulls />
              </LineChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
    </Dialog>
  )
}

function CreateNodeDialog({
  open,
  onClose,
  onCreated,
  defaultCapacity,
  usedSubnets,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
  defaultCapacity: number
  usedSubnets: string[]
}) {
  const { push } = useToast()
  const [name, setName] = useState('')
  const [nodeGroup, setNodeGroup] = useState('default')
  const [region, setRegion] = useState('')
  const [publicEndpoint, setPublicEndpoint] = useState('')
  const [subnetChoice, setSubnetChoice] = useState('') // a recommended CIDR or 'custom'
  const [customSubnet, setCustomSubnet] = useState('')
  const [capacity, setCapacity] = useState(defaultCapacity.toString())
  const [error, setError] = useState<string | null>(null)

  const usedSet = useMemo(() => new Set(usedSubnets), [usedSubnets])
  // Default to the first recommended subnet not already claimed by another node, so two
  // nodes never silently land on the same range; fall back to Custom if all are taken.
  const firstFreeSubnet = () => ALL_RECOMMENDED_SUBNETS.find((s) => !usedSet.has(s)) ?? 'custom'

  const wgSubnet = subnetChoice === 'custom' ? customSubnet.trim() : subnetChoice

  // The dialog stays mounted (just hidden) between opens, so a plain useState
  // initializer only ever sees defaultCapacity from the very first render - before
  // the settings fetch has necessarily resolved. Re-sync on every open instead, which
  // also gives a clean form each time rather than whatever was left over.
  useEffect(() => {
    if (!open) return
    setName('')
    setNodeGroup('default')
    setRegion('')
    setPublicEndpoint('')
    setSubnetChoice(firstFreeSubnet())
    setCustomSubnet('')
    setCapacity(defaultCapacity.toString())
    setError(null)
    // firstFreeSubnet reads usedSet; re-run when the set of taken subnets changes too.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, defaultCapacity, usedSet])

  const createMutation = useMutation({
    mutationFn: () =>
      apiFetch('/api/v1/nodes', {
        method: 'POST',
        body: JSON.stringify({
          name,
          node_group: nodeGroup,
          region,
          public_endpoint: publicEndpoint,
          wg_subnet: wgSubnet,
          capacity_max_peers: Number(capacity),
        }),
      }),
    onSuccess: () => {
      push('success', `Node "${name}" created`)
      onCreated()
    },
    onError: (err) => setError(err instanceof ApiError ? err.message : 'Failed to create node'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    createMutation.mutate()
  }

  return (
    <Dialog open={open} onClose={onClose} title="New node">
      <form onSubmit={handleSubmit} className="space-y-4">
        <Field label="Name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="e.g. eu-west-1" />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Node group">
            <Input value={nodeGroup} onChange={(e) => setNodeGroup(e.target.value)} placeholder="default" />
          </Field>
          <Field label="Capacity (peers)">
            <Input type="number" min="1" value={capacity} onChange={(e) => setCapacity(e.target.value)} />
          </Field>
          <Field
            label="Region"
            hint="Optional steering label - clients asking for this region are preferred onto this node."
          >
            <Input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="e.g. eu, us-east" />
          </Field>
        </div>
        <Field label="Public endpoint">
          <Input
            value={publicEndpoint}
            onChange={(e) => setPublicEndpoint(e.target.value)}
            required
            placeholder="vpn1.example.com:51820"
          />
        </Field>
        <Field
          label="WireGuard subnet"
          hint="The private range peer IPs are allocated from on this node. Each node needs its own — ranges already used by another node are disabled. These avoid the ranges home routers use, so clients don't lose their LAN."
        >
          <Select value={subnetChoice} onChange={(e) => setSubnetChoice(e.target.value)} required>
            {SUBNET_GROUPS.map((g) => (
              <optgroup key={g.label} label={g.label}>
                {g.subnets.map((s) => {
                  const used = usedSet.has(s)
                  return (
                    <option key={s} value={s} disabled={used}>
                      {s} · ≈{subnetPeerCapacity(s)} clients{used ? ' · in use' : ''}
                    </option>
                  )
                })}
              </optgroup>
            ))}
            <option value="custom">Custom…</option>
          </Select>
          {subnetChoice === 'custom' && (
            <Input
              className="mt-2"
              value={customSubnet}
              onChange={(e) => setCustomSubnet(e.target.value)}
              required
              placeholder="10.66.0.0/24"
            />
          )}
        </Field>
        {error && <p className="text-sm text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={createMutation.isPending}>
            {createMutation.isPending ? 'Creating…' : 'Create node'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
