import { Fragment, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ScrollText, ChevronDown, ChevronRight } from 'lucide-react'
import { apiFetch } from '../lib/api'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Select } from '../components/ui/Select'
import { EmptyState } from '../components/ui/EmptyState'
import { TableSkeleton } from '../components/ui/Skeleton'
import { Table, THead, Th, Tr, Td } from '../components/ui/Table'

interface AuditLogEntry {
  id: number
  actor: string
  action: string
  target: string | null
  detail: Record<string, unknown> | null
  ip_address: string | null
  created_at: string
}

export function AuditLogPage() {
  const [limit, setLimit] = useState(50)
  const [expanded, setExpanded] = useState<number | null>(null)

  const logQuery = useQuery({
    queryKey: ['audit-log', limit],
    queryFn: () => apiFetch<{ entries: AuditLogEntry[] | null }>(`/api/v1/audit-log?limit=${limit}`),
  })
  const entries = logQuery.data?.entries ?? []

  return (
    <div>
      <PageHeader
        title="Audit Log"
        description="Every admin and API-key action recorded against this panel."
        action={
          <Select value={limit} onChange={(e) => setLimit(Number(e.target.value))} className="w-40">
            <option value={20}>Last 20</option>
            <option value={50}>Last 50</option>
            <option value={100}>Last 100</option>
            <option value={200}>Last 200</option>
          </Select>
        }
      />

      <Card className="overflow-hidden">
        {logQuery.isLoading && <TableSkeleton cols={4} />}
        {logQuery.isError && <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load audit log.</p>}
        {!logQuery.isLoading && !logQuery.isError && entries.length === 0 && (
          <EmptyState icon={ScrollText} title="No audit entries yet" />
        )}
        {!logQuery.isLoading && !logQuery.isError && entries.length > 0 && (
          <Table>
            <THead>
              <tr>
                <Th className="w-8" />
                <Th>Time</Th>
                <Th>Actor</Th>
                <Th>Action</Th>
                <Th>Target</Th>
              </tr>
            </THead>
            <tbody>
              {entries.map((entry) => (
                <Fragment key={entry.id}>
                  <Tr interactive onClick={() => setExpanded(expanded === entry.id ? null : entry.id)}>
                    <Td className="text-faint">
                      {expanded === entry.id ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                    </Td>
                    <Td className="whitespace-nowrap text-muted tabular-nums">
                      {new Date(entry.created_at).toLocaleString()}
                    </Td>
                    <Td className="font-medium text-fg">{entry.actor}</Td>
                    <Td>
                      <code className="rounded-md bg-inset px-1.5 py-0.5 font-mono text-xs text-fg">{entry.action}</code>
                    </Td>
                    <Td className="text-muted">{entry.target ?? '—'}</Td>
                  </Tr>
                  {expanded === entry.id && (
                    <Tr className="bg-inset/40">
                      <Td colSpan={5} className="py-4">
                        <div className="grid grid-cols-1 gap-4 text-xs sm:grid-cols-2">
                          <div>
                            <p className="mb-1.5 font-semibold tracking-wider text-faint uppercase">IP address</p>
                            <p className="font-mono text-fg">{entry.ip_address ?? 'unknown'}</p>
                          </div>
                          <div>
                            <p className="mb-1.5 font-semibold tracking-wider text-faint uppercase">Detail</p>
                            <pre className="font-mono leading-relaxed whitespace-pre-wrap text-fg">
                              {entry.detail ? JSON.stringify(entry.detail, null, 2) : '—'}
                            </pre>
                          </div>
                        </div>
                      </Td>
                    </Tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </div>
  )
}
