import { Dialog } from './Dialog'
import { Button } from './Button'

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmLabel = 'Confirm',
  danger = false,
  submitting = false,
}: {
  open: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  description: string
  confirmLabel?: string
  danger?: boolean
  submitting?: boolean
}) {
  return (
    <Dialog open={open} onClose={onClose} title={title}>
      <p className="text-sm leading-relaxed text-muted">{description}</p>
      <div className="mt-6 flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose} disabled={submitting}>
          Cancel
        </Button>
        <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={submitting}>
          {submitting ? 'Working…' : confirmLabel}
        </Button>
      </div>
    </Dialog>
  )
}
