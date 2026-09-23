import { useQuery } from '@tanstack/react-query'
import { Server, Users, ShieldAlert, Activity, HardDrive, Wifi } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { PageHeader } from '../components/ui/PageHeader'
import { StatCard } from '../components/ui/StatCard'
import { Card } from '../components/ui/Card'
import { Badge, statusTone } from '../components/ui/Badge'
import { Skeleton } from '../components/ui/Skeleton'
import { Progress } from '../components/ui/Progress'

interface Node {
  id: string
  name: string
  node_group: string
  status: string
  capacity_max_peers: number
}

interface AccountPeer {
  node_id: string
  online: boolean
}

interface Account {
  id: string
  label: string
  status: string
  data_used_bytes: number
  data_quota_bytes: number | null
  peers: AccountPeer[]
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

export function DashboardPage() {
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiFetch<{ nodes: Node[] | null }>('/api/v1/nodes'),
  })
  const accountsQuery = useQuery({
    queryKey: ['accounts'],
    queryFn: () => apiFetch<{ accounts: Account[] | null }>('/api/v1/accounts'),
  })

  const nodes = nodesQuery.data?.nodes ?? []
  const accounts = accountsQuery.data?.accounts ?? []
  const isLoading = nodesQuery.isLoading || accountsQuery.isLoading

  const onlineNodes = nodes.filter((n) => n.status === 'online' || n.status === 'registered').length
  const activeAccounts = accounts.filter((a) => a.status === 'active').length
  const suspendedAccounts = accounts.filter((a) => a.status === 'suspended').length
  const totalDataUsed = accounts.reduce((sum, a) => sum + (a.data_used_bytes ?? 0), 0)
  // "Online" here means at least one of the account's peers has handshaked recently
  // (server-computed, see accountPeerResponse.online) - an account with peers on
  // several nodes is online as soon as any one of them is actively connected.
  const onlineAccounts = accounts.filter((a) => a.peers?.some((p) => p.online)).length

  // Peers provisioned per node (an account contributes one peer to each node
  // it's on), so capacity bars show real utilization rather than just the cap.
  const peersPerNode = new Map<string, number>()
  for (const account of accounts) {
    for (const peer of account.peers ?? []) {
      peersPerNode.set(peer.node_id, (peersPerNode.get(peer.node_id) ?? 0) + 1)
    }
  }

  return (
    <div>
      <PageHeader title="Dashboard" description="Overview of your WireGuard fleet." />

      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-28" />
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
          <StatCard icon={Server} label="Nodes online" value={`${onlineNodes} / ${nodes.length}`} tone="blue" />
          <StatCard icon={Wifi} label="Accounts online" value={`${onlineAccounts} / ${accounts.length}`} tone="green" />
          <StatCard icon={Users} label="Active accounts" value={activeAccounts} tone="green" />
          <StatCard icon={ShieldAlert} label="Suspended accounts" value={suspendedAccounts} tone="red" />
          <StatCard icon={Activity} label="Total accounts" value={accounts.length} tone="slate" />
          <StatCard icon={HardDrive} label="Data transferred" value={formatBytes(totalDataUsed)} tone="amber" />
        </div>
      )}

      <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Card>
          <div className="border-b border-edge px-6 py-4">
            <h2 className="text-sm font-semibold tracking-tight text-fg">Node capacity</h2>
          </div>
          <div className="p-6">
            {nodes.length === 0 ? (
              <p className="text-sm text-muted">No nodes registered yet.</p>
            ) : (
              <div className="space-y-5">
                {nodes.map((n) => {
                  const used = peersPerNode.get(n.id) ?? 0
                  return (
                    <div key={n.id}>
                      <div className="mb-2 flex items-center justify-between gap-4 text-sm">
                        <div className="flex min-w-0 items-center gap-2">
                          <span className="truncate font-medium text-fg">{n.name}</span>
                          <Badge dot tone={statusTone(n.status)}>{n.status}</Badge>
                        </div>
                        <span className="shrink-0 text-xs text-muted tabular-nums">
                          {used} / {n.capacity_max_peers} peers
                        </span>
                      </div>
                      <Progress value={used} max={n.capacity_max_peers} />
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </Card>

        <Card>
          <div className="border-b border-edge px-6 py-4">
            <h2 className="text-sm font-semibold tracking-tight text-fg">Recent accounts</h2>
          </div>
          <div className="p-6">
            {accounts.length === 0 ? (
              <p className="text-sm text-muted">No accounts yet.</p>
            ) : (
              <div className="space-y-4">
                {accounts.slice(0, 6).map((a) => {
                  const isOnline = a.peers?.some((p) => p.online)
                  return (
                    <div key={a.id} className="flex items-center justify-between gap-4 text-sm">
                      <span className="inline-flex min-w-0 items-center gap-2 font-medium text-fg">
                        <span
                          className={`h-2 w-2 shrink-0 rounded-full ${isOnline ? 'bg-emerald-500' : 'bg-edge-strong'}`}
                          title={isOnline ? 'Online' : 'Offline'}
                        />
                        <span className="truncate">{a.label}</span>
                      </span>
                      <span className="flex shrink-0 items-center gap-3">
                        <span className="text-xs text-muted tabular-nums">{formatBytes(a.data_used_bytes ?? 0)}</span>
                        <Badge tone={statusTone(a.status)}>{a.status}</Badge>
                      </span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </Card>
      </div>
    </div>
  )
}
