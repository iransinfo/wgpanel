import type { HTMLAttributes } from 'react'

export function Card({ className = '', ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={`rounded-xl border border-edge bg-surface shadow-[0_1px_2px_rgb(0_0_0/0.04)] ${className}`}
      {...props}
    />
  )
}
