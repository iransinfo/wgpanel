import type { LucideIcon } from 'lucide-react'
import { Card } from './Card'

const toneClasses: Record<string, string> = {
  slate: 'bg-inset text-muted',
  green: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  red: 'bg-rose-500/10 text-rose-600 dark:text-rose-400',
  amber: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
  blue: 'bg-sky-500/10 text-sky-600 dark:text-sky-400',
}

export function StatCard({
  icon: Icon,
  label,
  value,
  tone = 'slate',
}: {
  icon: LucideIcon
  label: string
  value: string | number
  tone?: 'slate' | 'green' | 'red' | 'amber' | 'blue'
}) {
  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-3">
        <p className="text-[13px] font-medium text-muted">{label}</p>
        <div className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${toneClasses[tone]}`}>
          <Icon className="h-4 w-4" />
        </div>
      </div>
      <p className="mt-2 text-2xl font-semibold tracking-tight text-fg tabular-nums">{value}</p>
    </Card>
  )
}
