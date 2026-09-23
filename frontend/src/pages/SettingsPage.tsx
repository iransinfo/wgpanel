import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { DatabaseBackup, Download, Save, ShieldCheck, SlidersHorizontal, Upload } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { apiFetch, ApiError } from '../lib/api'
import { getAccessToken } from '../lib/tokenStore'
import { useAuth } from '../lib/auth'
import { useToast } from '../lib/toast'
import { PageHeader } from '../components/ui/PageHeader'
import { Card } from '../components/ui/Card'
import { Button } from '../components/ui/Button'
import { Input } from '../components/ui/Input'
import { Field } from '../components/ui/Field'
import { Skeleton } from '../components/ui/Skeleton'
import { Dialog } from '../components/ui/Dialog'

interface Settings {
  public_base_url: string | null
  default_data_quota_gb: number | null
  default_device_limit: number | null
  default_node_capacity: number
  support_contact: string | null
  panel_domain: string | null
  client_dns: string
  sub_domain: string | null
  sub_port: number | null
}

interface UpdateSettingsResult extends Settings {
  domain_live_applied: boolean
  domain_apply_error: string | null
}

interface RestoreResult {
  restored: Record<string, number>
  ca_restored: boolean
  restart_required: boolean
}

function BackupCard() {
  const { push } = useToast()
  const { logout } = useAuth()
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)

  const [downloadOpen, setDownloadOpen] = useState(false)
  const [downloadPassword, setDownloadPassword] = useState('')
  const [downloadPasswordConfirm, setDownloadPasswordConfirm] = useState('')
  const [downloading, setDownloading] = useState(false)

  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [restorePassword, setRestorePassword] = useState('')
  const [restoring, setRestoring] = useState(false)
  const [restoreResult, setRestoreResult] = useState<RestoreResult | null>(null)

  function closeDownload() {
    setDownloadOpen(false)
    setDownloadPassword('')
    setDownloadPasswordConfirm('')
  }

  function closeRestore() {
    setRestoreFile(null)
    setRestorePassword('')
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  async function downloadBackup() {
    setDownloading(true)
    try {
      const res = await fetch('/api/v1/backup', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${getAccessToken() ?? ''}`,
        },
        body: JSON.stringify({ password: downloadPassword }),
      })
      if (!res.ok) {
        let message = 'Failed to create backup'
        try {
          const body = (await res.json()) as { error?: { message?: string } }
          message = body?.error?.message ?? message
        } catch {
          // Not JSON - keep the generic message.
        }
        throw new Error(message)
      }
      const filename =
        res.headers.get('Content-Disposition')?.match(/filename="?([^";]+)"?/)?.[1] ?? 'wgpanel-backup.json'
      const url = URL.createObjectURL(await res.blob())
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      a.click()
      URL.revokeObjectURL(url)
      closeDownload()
      push('success', 'Backup downloaded - keep the file AND its password safe; the password cannot be recovered')
    } catch (err) {
      push('error', err instanceof Error ? err.message : 'Failed to create backup')
    } finally {
      setDownloading(false)
    }
  }

  async function restoreBackup() {
    if (!restoreFile) return
    setRestoring(true)
    try {
      const backup = JSON.parse(await restoreFile.text()) as unknown
      const result = await apiFetch<RestoreResult>('/api/v1/backup/restore', {
        method: 'POST',
        body: JSON.stringify({ password: restorePassword, backup }),
      })
      setRestoreResult(result)
      // Everything cached client-side describes the pre-restore panel.
      queryClient.clear()
      closeRestore()
      push('success', 'Backup restored')
    } catch (err) {
      if (err instanceof SyntaxError) {
        push('error', 'That file is not a WGPanel backup')
      } else {
        push('error', err instanceof ApiError ? err.message : 'Restore failed')
      }
    } finally {
      setRestoring(false)
    }
  }

  return (
    <Card className="overflow-hidden">
      <SectionHeader
        icon={DatabaseBackup}
        title="Backup & restore"
        description="One password-encrypted file with everything: admins, nodes, accounts and their keys, API keys, settings, audit log, the node CA, and the encryption keys from deploy/.env. Restoring needs only this file and its password - even on a brand-new server."
      />
      <div className="space-y-4 p-6">
        <div className="flex items-center justify-between gap-4">
          <p className="text-sm leading-relaxed text-muted">
            The file is useless without the password you choose - and the password cannot be recovered, so store both
            safely. Metrics history (usage charts) is not included; account usage totals are.
          </p>
          <Button variant="secondary" onClick={() => setDownloadOpen(true)}>
            <Download className="h-4 w-4" />
            Download backup
          </Button>
        </div>

        <div className="flex items-center justify-between gap-4 border-t border-edge pt-4">
          <p className="text-sm leading-relaxed text-muted">
            Restore replaces <span className="font-semibold text-fg">all</span> current panel data with the file's
            contents - including admin users, so your own login may change. Works on a fresh install with new{' '}
            <code>.env</code> keys: account keys are re-encrypted automatically.
          </p>
          <input
            ref={fileInputRef}
            type="file"
            accept="application/json,.json"
            className="hidden"
            onChange={(e) => setRestoreFile(e.target.files?.[0] ?? null)}
          />
          <Button variant="danger" onClick={() => fileInputRef.current?.click()} disabled={restoring}>
            <Upload className="h-4 w-4" />
            Restore backup
          </Button>
        </div>

        {restoreResult && (
          <div className="rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2.5 text-sm leading-relaxed text-emerald-700 dark:text-emerald-400">
            <p>
              Restored {restoreResult.restored.accounts ?? 0} accounts, {restoreResult.restored.nodes ?? 0} nodes,{' '}
              {restoreResult.restored.admins ?? 0} admins and {restoreResult.restored.api_keys ?? 0} API keys.
              {restoreResult.restart_required &&
                ' The node CA changed - restart the api container (wgpanel restart) so agents can reconnect.'}{' '}
              Log in again with the restored credentials.
            </p>
            <Button variant="secondary" size="sm" className="mt-2" onClick={logout}>
              Log out now
            </Button>
          </div>
        )}
      </div>

      <Dialog open={downloadOpen} onClose={closeDownload} title="Encrypt backup">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            downloadBackup()
          }}
          className="space-y-4"
        >
          <p className="text-sm leading-relaxed text-muted">
            Choose a password for this backup. Restoring it - here or on a new server - requires exactly this
            password; it is not stored anywhere and cannot be recovered.
          </p>
          <Field label="Backup password">
            <Input
              type="password"
              value={downloadPassword}
              onChange={(e) => setDownloadPassword(e.target.value)}
              minLength={8}
              placeholder="At least 8 characters"
              autoFocus
              required
            />
          </Field>
          <Field label="Confirm password">
            <Input
              type="password"
              value={downloadPasswordConfirm}
              onChange={(e) => setDownloadPasswordConfirm(e.target.value)}
              required
            />
          </Field>
          {downloadPasswordConfirm.length > 0 && downloadPassword !== downloadPasswordConfirm && (
            <p className="text-sm text-rose-600 dark:text-rose-400">Passwords do not match.</p>
          )}
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" type="button" onClick={closeDownload} disabled={downloading}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={downloading || downloadPassword.length < 8 || downloadPassword !== downloadPasswordConfirm}
            >
              <Download className="h-4 w-4" />
              {downloading ? 'Encrypting…' : 'Download'}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={restoreFile !== null} onClose={closeRestore} title="Replace all panel data?">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            restoreBackup()
          }}
          className="space-y-4"
        >
          <p className="text-sm leading-relaxed text-muted">
            Everything currently in this panel - accounts, nodes, admins, API keys, settings, audit log - will be
            replaced by <span className="font-medium text-fg">{restoreFile?.name}</span>. This cannot be undone.
            Download a backup of the current state first if you might need it.
          </p>
          <Field label="Backup password">
            <Input
              type="password"
              value={restorePassword}
              onChange={(e) => setRestorePassword(e.target.value)}
              placeholder="The password this backup was encrypted with"
              autoFocus
              required
            />
          </Field>
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="secondary" type="button" onClick={closeRestore} disabled={restoring}>
              Cancel
            </Button>
            <Button variant="danger" type="submit" disabled={restoring || restorePassword.length === 0}>
              {restoring ? 'Restoring…' : 'Replace everything'}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  )
}

function SectionHeader({ icon: Icon, title, description }: { icon: LucideIcon; title: string; description: string }) {
  return (
    <div className="flex items-start gap-3 border-b border-edge px-6 py-4">
      <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-fg">
        <Icon className="h-4 w-4" />
      </div>
      <div>
        <h2 className="text-sm font-semibold tracking-tight text-fg">{title}</h2>
        <p className="mt-0.5 text-xs leading-relaxed text-muted">{description}</p>
      </div>
    </div>
  )
}

export function SettingsPage() {
  const queryClient = useQueryClient()
  const { push } = useToast()

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => apiFetch<Settings>('/api/v1/settings'),
  })

  const [publicBaseUrl, setPublicBaseUrl] = useState('')
  const [defaultQuotaGb, setDefaultQuotaGb] = useState('')
  const [defaultDeviceLimit, setDefaultDeviceLimit] = useState('')
  const [defaultNodeCapacity, setDefaultNodeCapacity] = useState('250')
  const [supportContact, setSupportContact] = useState('')
  const [clientDns, setClientDns] = useState('1.1.1.1, 1.0.0.1')
  const [panelDomain, setPanelDomain] = useState('')
  const [subDomain, setSubDomain] = useState('')
  const [subPort, setSubPort] = useState('')
  const [domainApplyError, setDomainApplyError] = useState<string | null>(null)
  const [domainLiveApplied, setDomainLiveApplied] = useState(false)

  useEffect(() => {
    if (!settingsQuery.data) return
    setPublicBaseUrl(settingsQuery.data.public_base_url ?? '')
    setDefaultQuotaGb(settingsQuery.data.default_data_quota_gb?.toString() ?? '')
    setDefaultDeviceLimit(settingsQuery.data.default_device_limit?.toString() ?? '')
    setDefaultNodeCapacity(settingsQuery.data.default_node_capacity.toString())
    setSupportContact(settingsQuery.data.support_contact ?? '')
    setClientDns(settingsQuery.data.client_dns)
    setPanelDomain(settingsQuery.data.panel_domain ?? '')
    setSubDomain(settingsQuery.data.sub_domain ?? '')
    setSubPort(settingsQuery.data.sub_port?.toString() ?? '')
  }, [settingsQuery.data])

  const saveMutation = useMutation({
    mutationFn: () =>
      apiFetch<Settings>('/api/v1/settings', {
        method: 'PATCH',
        body: JSON.stringify({
          public_base_url: publicBaseUrl || null,
          default_data_quota_gb: defaultQuotaGb ? Number(defaultQuotaGb) : null,
          default_device_limit: defaultDeviceLimit ? Number(defaultDeviceLimit) : null,
          default_node_capacity: Number(defaultNodeCapacity),
          support_contact: supportContact || null,
          client_dns: clientDns || null,
        }),
      }),
    onSuccess: (settings) => {
      push('success', 'Settings saved')
      queryClient.setQueryData(['settings'], settings)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to save settings'),
  })

  const domainMutation = useMutation({
    mutationFn: () =>
      apiFetch<UpdateSettingsResult>('/api/v1/settings', {
        method: 'PATCH',
        body: JSON.stringify({
          panel_domain: panelDomain,
          // Empty field = feature off / default port: the API treats "" and 0 as an
          // explicit reset to NULL (unlike omitting the key, which means unchanged).
          sub_domain: subDomain,
          sub_port: subPort ? Number(subPort) : 0,
        }),
      }),
    onSuccess: (settings) => {
      setDomainLiveApplied(settings.domain_live_applied)
      setDomainApplyError(settings.domain_apply_error)
      if (settings.domain_live_applied) {
        const domains = settings.sub_domain ? `${panelDomain} and ${settings.sub_domain}` : panelDomain
        push('success', `Domains updated - Caddy is provisioning certificates for ${domains}`)
      } else {
        push('error', settings.domain_apply_error ?? 'Domains saved, but could not push them to Caddy live')
      }
      queryClient.setQueryData(['settings'], settings)
    },
    onError: (err) => push('error', err instanceof ApiError ? err.message : 'Failed to update domains'),
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    saveMutation.mutate()
  }

  function handleDomainSubmit(e: FormEvent) {
    e.preventDefault()
    setDomainApplyError(null)
    domainMutation.mutate()
  }

  return (
    <div>
      <PageHeader title="Settings" description="Panel-wide configuration and defaults." />

      <div className="max-w-2xl space-y-6">
        <Card className="overflow-hidden">
          <SectionHeader
            icon={ShieldCheck}
            title="Domain & TLS"
            description="The domains Caddy serves and automatically provisions Let's Encrypt certificates for. Changes take effect live via Caddy's admin API - no restart or redeploy needed. Requires DNS for each domain to already point at this server."
          />
          {settingsQuery.isLoading ? (
            <div className="p-6">
              <Skeleton className="h-10" />
            </div>
          ) : (
            <form onSubmit={handleDomainSubmit} className="space-y-4 p-6">
              <Field label="Panel domain">
                <Input value={panelDomain} onChange={(e) => setPanelDomain(e.target.value)} placeholder="panel.example.com" required />
              </Field>
              <div className="grid grid-cols-[1fr_8rem] gap-4">
                <Field
                  label="Subscription domain"
                  hint="Optional separate domain for the subscription links handed to end users. It serves only the subscription endpoints - the admin panel is never reachable through it - with its own automatic certificate. Leave empty to keep subscription links on the panel domain."
                >
                  <Input value={subDomain} onChange={(e) => setSubDomain(e.target.value)} placeholder="sub.example.com" />
                </Field>
                <Field
                  label="Subscription port"
                  hint="Default 443 works with no extra setup. Any other port must match SUB_PORT in deploy/.env (default 8443) and be open in the firewall."
                >
                  <Input
                    type="number"
                    min="1"
                    max="65535"
                    value={subPort}
                    onChange={(e) => setSubPort(e.target.value)}
                    placeholder="443"
                  />
                </Field>
              </div>
              {domainLiveApplied && !domainApplyError && (
                <p className="text-sm text-emerald-600 dark:text-emerald-400">Applied live - Caddy is now serving with this configuration.</p>
              )}
              {domainApplyError && (
                <p className="rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2.5 text-sm leading-relaxed text-amber-700 dark:text-amber-400">
                  Saved, but the live push to Caddy failed: {domainApplyError}. The API retries it automatically on its
                  next restart.
                </p>
              )}
              <div className="flex justify-end border-t border-edge pt-4">
                <Button type="submit" disabled={domainMutation.isPending || !panelDomain}>
                  <ShieldCheck className="h-4 w-4" />
                  {domainMutation.isPending ? 'Applying…' : 'Apply domains'}
                </Button>
              </div>
            </form>
          )}
        </Card>

        <Card className="overflow-hidden">
          <SectionHeader
            icon={SlidersHorizontal}
            title="Defaults & panel info"
            description="Baseline values applied to new nodes and accounts, plus what other admins see about this panel."
          />
          {settingsQuery.isLoading ? (
            <div className="space-y-4 p-6">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10" />
              ))}
            </div>
          ) : settingsQuery.isError ? (
            <p className="p-6 text-sm text-rose-600 dark:text-rose-400">Could not load settings.</p>
          ) : (
            <form onSubmit={handleSubmit} className="space-y-6 p-6">
              <Field
                label="Public base URL"
                hint="The domain this panel is served on (e.g. https://panel.example.com). Informational - changing it here does not move DNS/TLS, which is still configured in deploy/.env's PANEL_DOMAIN and requires a redeploy to actually change."
              >
                <Input
                  value={publicBaseUrl}
                  onChange={(e) => setPublicBaseUrl(e.target.value)}
                  placeholder="https://panel.example.com"
                />
              </Field>

              <div className="grid grid-cols-2 gap-4">
                <Field label="Default account data quota (GB)">
                  <Input
                    type="number"
                    min="0"
                    step="0.1"
                    value={defaultQuotaGb}
                    onChange={(e) => setDefaultQuotaGb(e.target.value)}
                    placeholder="Unlimited"
                  />
                </Field>
                <Field label="Default account device limit">
                  <Input
                    type="number"
                    min="1"
                    value={defaultDeviceLimit}
                    onChange={(e) => setDefaultDeviceLimit(e.target.value)}
                    placeholder="Unlimited"
                  />
                </Field>
              </div>

              <Field label="Default node capacity (max peers)">
                <Input
                  type="number"
                  min="1"
                  value={defaultNodeCapacity}
                  onChange={(e) => setDefaultNodeCapacity(e.target.value)}
                  required
                />
              </Field>

              <Field
                label="Client DNS server(s)"
                hint={
                  <>
                    Written into every generated WireGuard config's <code>DNS =</code> line (comma-separated). Since clients
                    full-tunnel, they can only resolve names through this server - if it's unreachable from where your nodes
                    egress, clients connect but have "no internet". The Cloudflare default is blocked on some networks; use a
                    resolver you've confirmed works from the node. Takes effect on the next config download.
                  </>
                }
              >
                <Input
                  value={clientDns}
                  onChange={(e) => setClientDns(e.target.value)}
                  placeholder="1.1.1.1, 1.0.0.1"
                  required
                />
              </Field>

              <Field
                label="Support contact"
                hint="Shown to other admins who need help (e.g. an email address or Telegram handle)."
              >
                <Input
                  value={supportContact}
                  onChange={(e) => setSupportContact(e.target.value)}
                  placeholder="ops@example.com"
                />
              </Field>

              <div className="flex justify-end border-t border-edge pt-4">
                <Button type="submit" disabled={saveMutation.isPending}>
                  <Save className="h-4 w-4" />
                  {saveMutation.isPending ? 'Saving…' : 'Save settings'}
                </Button>
              </div>
            </form>
          )}
        </Card>

        <BackupCard />
      </div>
    </div>
  )
}
