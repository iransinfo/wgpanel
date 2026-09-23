import type { HTMLAttributes } from 'react'

type Tone = 'green' | 'red' | 'amber' | 'slate' | 'blue'

const tones: Record<Tone, string> = {
  green: 'bg-emerald-500/10 text-emerald-700 ring-emerald-600/25 dark:text-emerald-400 dark:ring-emerald-400/25',
  red: 'bg-rose-500/10 text-rose-700 ring-rose-600/25 dark:text-rose-400 dark:ring-rose-400/25',
  amber: 'bg-amber-500/10 text-amber-700 ring-amber-600/30 dark:text-amber-400 dark:ring-amber-400/25',
  slate: 'bg-inset text-muted ring-edge-strong/60',
  blue: 'bg-sky-500/10 text-sky-700 ring-sky-600/25 dark:text-sky-400 dark:ring-sky-400/25',
}

const dots: Record<Tone, string> = {
  green: 'bg-emerald-500',
  red: 'bg-rose-500',
  amber: 'bg-amber-500',
  slate: 'bg-faint',
  blue: 'bg-sky-500',
}

export function Badge({
  tone = 'slate',
  dot = false,
  children,
  className = '',
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: Tone; dot?: boolean }) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${tones[tone]} ${className}`}
      {...props}
    >
      {dot && <span className={`h-1.5 w-1.5 rounded-full ${dots[tone]}`} />}
      {children}
    </span>
  )
}

/** Maps the various status strings this app uses (account/node/api-key) to a tone. */
export function statusTone(status: string): Tone {
  switch (status) {
    case 'active':
    case 'online':
    case 'registered':
      return 'green'
    case 'suspended':
    case 'offline':
    case 'revoked':
      return 'red'
    case 'pending':
      return 'amber'
    default:
      return 'slate'
  }
}
