import type { SelectHTMLAttributes } from 'react'
import { ChevronDown } from 'lucide-react'

export function Select({ className = '', ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className={`relative ${className}`}>
      <select
        className="h-9 w-full cursor-pointer appearance-none rounded-lg border border-edge-strong/70 bg-transparent py-0 pr-9 pl-3 text-sm text-fg shadow-xs transition-[border-color,box-shadow] hover:border-edge-strong focus:border-accent focus:ring-2 focus:ring-accent/25 focus:outline-none [&>option]:bg-surface [&>option]:text-fg"
        {...props}
      />
      <ChevronDown className="pointer-events-none absolute top-1/2 right-3 h-4 w-4 -translate-y-1/2 text-faint" />
    </div>
  )
}
