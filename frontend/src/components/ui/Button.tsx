import type { ButtonHTMLAttributes } from 'react'

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'md' | 'sm' | 'icon'
}

const variants = {
  primary:
    'bg-accent text-white shadow-sm hover:bg-accent-hover active:bg-accent-hover',
  secondary:
    'border border-edge-strong/70 bg-surface text-fg shadow-xs hover:bg-inset hover:border-edge-strong',
  ghost: 'text-muted hover:bg-inset hover:text-fg',
  danger: 'bg-rose-600 text-white shadow-sm hover:bg-rose-500 active:bg-rose-500',
}

const sizes = {
  md: 'h-9 px-4 text-sm',
  sm: 'h-8 px-2.5 text-xs',
  icon: 'h-8 w-8',
}

export function Button({ variant = 'primary', size = 'md', className = '', ...props }: ButtonProps) {
  return (
    <button
      className={`inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-lg font-medium transition-[background-color,border-color,box-shadow] outline-none focus-visible:ring-2 focus-visible:ring-accent/50 disabled:pointer-events-none disabled:opacity-50 ${variants[variant]} ${sizes[size]} ${className}`}
      {...props}
    />
  )
}
