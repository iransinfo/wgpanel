import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import { CheckCircle2, XCircle } from 'lucide-react'

interface Toast {
  id: number
  kind: 'success' | 'error'
  message: string
}

interface ToastContextValue {
  push: (kind: Toast['kind'], message: string) => void
}

const ToastContext = createContext<ToastContextValue | undefined>(undefined)

let nextId = 1

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const push = useCallback((kind: Toast['kind'], message: string) => {
    const id = nextId++
    setToasts((prev) => [...prev, { id, kind, message }])
    setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id))
    }, 4000)
  }, [])

  return (
    <ToastContext.Provider value={{ push }}>
      {children}
      <div className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-full max-w-sm flex-col gap-2">
        {toasts.map((t) => (
          <div
            key={t.id}
            role="status"
            className="animate-toast-in pointer-events-auto flex items-start gap-2.5 rounded-xl border border-edge bg-surface px-4 py-3 text-sm font-medium text-fg shadow-lg shadow-slate-950/10"
          >
            {t.kind === 'success' ? (
              <CheckCircle2 className="mt-px h-4.5 w-4.5 shrink-0 text-emerald-500" />
            ) : (
              <XCircle className="mt-px h-4.5 w-4.5 shrink-0 text-rose-500" />
            )}
            {t.message}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within ToastProvider')
  return ctx
}
