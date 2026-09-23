import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from 'react'

/** Data-table primitives shared by every list page. Wraps the table in a
 * horizontal scroll container so wide tables never break the page layout. */
export function Table({ className = '', ...props }: HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="overflow-x-auto">
      <table className={`w-full text-left text-sm ${className}`} {...props} />
    </div>
  )
}

export function THead({ children }: { children: ReactNode }) {
  return (
    <thead className="border-b border-edge bg-inset/60 text-xs tracking-wide text-muted uppercase">
      {children}
    </thead>
  )
}

export function Th({ className = '', ...props }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={`px-6 py-3 font-medium ${className}`} {...props} />
}

export function Tr({
  interactive = false,
  className = '',
  ...props
}: HTMLAttributes<HTMLTableRowElement> & { interactive?: boolean }) {
  return (
    <tr
      className={`border-b border-edge/70 transition-colors last:border-0 ${
        interactive ? 'cursor-pointer hover:bg-inset/60' : ''
      } ${className}`}
      {...props}
    />
  )
}

export function Td({ className = '', ...props }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={`px-6 py-3.5 ${className}`} {...props} />
}
