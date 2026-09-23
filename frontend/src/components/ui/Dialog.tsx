import { useEffect, type ReactNode } from 'react'
import { X } from 'lucide-react'

// Body-scroll lock is ref-counted at module scope so stacked dialogs (e.g. the delete
// ConfirmDialog opened on top of an account detail Dialog) don't race on
// document.body.style.overflow: the body stays locked until the LAST open dialog
// closes, regardless of the order they unmount in. A single dialog restoring the
// pre-lock value directly would otherwise re-enable scrolling while another is still
// open, or leave the body permanently locked if the two unmount in the wrong order.
let openDialogCount = 0
let bodyOverflowBeforeLock = ''

function lockBodyScroll() {
  if (openDialogCount === 0) {
    bodyOverflowBeforeLock = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
  openDialogCount++
}

function unlockBodyScroll() {
  openDialogCount = Math.max(0, openDialogCount - 1)
  if (openDialogCount === 0) {
    document.body.style.overflow = bodyOverflowBeforeLock
  }
}

interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  children: ReactNode
  /** Tailwind max-width class - defaults to max-w-md. Widen it (e.g. max-w-2xl) for
   * content-heavy dialogs like the account/node detail views (peer list + chart +
   * QR/config side-by-side) that get uncomfortably cramped at the default width. */
  maxWidthClassName?: string
}

export function Dialog({ open, onClose, title, children, maxWidthClassName = 'max-w-md' }: DialogProps) {
  // Escape closes; page behind the overlay stays put instead of scrolling.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    lockBodyScroll()
    return () => {
      document.removeEventListener('keydown', onKey)
      unlockBodyScroll()
    }
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      className="animate-backdrop-in fixed inset-0 z-40 flex items-center justify-center overflow-y-auto bg-slate-950/50 p-4 backdrop-blur-[2px]"
      onClick={onClose}
    >
      {/* flex-col + max-h caps the dialog to the viewport and keeps the header
          (title/close button) always reachable - previously an unbounded panel grew
          taller than the viewport with nothing scrollable, pushing the header and
          the content's own bottom action buttons off-screen with no way to reach
          either (see the account detail dialog with 5 node peers + chart + QR). */}
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={`animate-dialog-in flex max-h-[85vh] w-full flex-col rounded-2xl border border-edge bg-surface shadow-2xl shadow-slate-950/20 ${maxWidthClassName}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-edge px-6 py-4">
          <h2 className="text-base font-semibold tracking-tight text-fg">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="-mr-1.5 cursor-pointer rounded-lg p-1.5 text-faint transition-colors hover:bg-inset hover:text-fg focus-visible:ring-2 focus-visible:ring-accent/50 focus-visible:outline-none"
            aria-label="Close"
          >
            <X className="h-4.5 w-4.5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-6">{children}</div>
      </div>
    </div>
  )
}
