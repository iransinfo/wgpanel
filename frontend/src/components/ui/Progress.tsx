/** Thin usage/capacity bar. Color shifts as the value approaches the max so
 * near-full nodes/quotas stand out at a glance. */
export function Progress({ value, max, className = '' }: { value: number; max: number; className?: string }) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0
  const tone = pct >= 90 ? 'bg-rose-500' : pct >= 70 ? 'bg-amber-500' : 'bg-accent'
  return (
    <div className={`h-1.5 w-full overflow-hidden rounded-full bg-inset ${className}`}>
      <div className={`h-full rounded-full transition-[width] duration-500 ${tone}`} style={{ width: `${pct}%` }} />
    </div>
  )
}
