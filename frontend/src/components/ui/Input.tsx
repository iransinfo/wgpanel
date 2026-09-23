import type { InputHTMLAttributes } from 'react'

export function Input({ className = '', ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={`h-9 w-full rounded-lg border border-edge-strong/70 bg-transparent px-3 text-sm text-fg shadow-xs transition-[border-color,box-shadow] placeholder:text-faint hover:border-edge-strong focus:border-accent focus:ring-2 focus:ring-accent/25 focus:outline-none disabled:cursor-not-allowed disabled:bg-inset disabled:text-muted ${className}`}
      {...props}
    />
  )
}
