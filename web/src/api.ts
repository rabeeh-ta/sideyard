export type RuntimeState = 'running' | 'crashed' | 'stopped' | 'not-deployed'

export type RuntimeStatus = {
  state: RuntimeState
  instance?: string
  pid?: number
  port?: number
  startedAt?: string
  restarts: number
  memoryBytes: number
  cpuPercent: number
  health: { status: 'healthy' | 'unhealthy' | 'unknown'; checkedAt?: string; error?: string }
}

export type Release = {
  number: number
  commit: string
  message: string
  size: number
  sha256: string
  status: 'starting' | 'active' | 'failed' | 'superseded'
  log: string
  pruned: boolean
  createdAt: string
}

export type Project = {
  name: string
  healthPath: string
  args: string
  memoryMB: number
  cpuPercent: number
  desiredState: 'running' | 'stopped'
  dataBytes: number
  createdAt: string
  domains: string[]
  status: RuntimeStatus
  activeRelease: Release | null
  releases?: Release[]
}

export type Builtin = { key: string; description: string }

export type ServerInfo = {
  domain: string
  dashboardHost: string
  dev: boolean
  publicAddr: string
  runner: 'systemd' | 'process'
  version: string
  arch: 'amd64' | 'arm64' | string
  resources: {
    cpus: number
    cpuPercent: number
    load1: number
    memTotalBytes: number
    memAvailableBytes: number
    diskTotalBytes: number
    diskUsedBytes: number
  }
}

export type Database = { path: string; size: number; modifiedAt: string }

export type Settings = { healthPath: string; args: string; memoryMB: number; cpuPercent: number }

export type StorageInfo = {
  connected: boolean
  endpoint: string
  region: string
  bucket: string
  accessKey: string
  updatedAt?: string
}

export type StorageInput = { endpoint: string; region: string; bucket: string; accessKey: string; secretKey: string }

export type S3Object = { key: string; size: number; modifiedAt?: string; isPrefix?: boolean }

export type JobSpec = { name: string; schedule: string; timeoutSec: number; retries: number; enabled: boolean }

export type JobRun = {
  id: number
  trigger: 'schedule' | 'catch-up' | 'manual'
  scheduledAt: string
  startedAt: string
  finishedAt?: string
  status: 'running' | 'succeeded' | 'failed' | 'skipped'
  attempts: number
  httpStatus: number
  output: string
}

export type Job = JobSpec & { path: string; nextRun?: string; running: boolean; lastRun: JobRun | null }

export type Column = {
  name: string
  type: string
  notNull: boolean
  default: string | null
  pk: number
  generated: boolean
}

export type TableInfo = {
  name: string
  type: 'table' | 'view' | 'virtual'
  rows: number
  withoutRowid: boolean
  columns: Column[]
  indexes: { name: string; unique: boolean; columns: string[] }[]
  sql: string
}

/** A cell as the API sends it: blobs as their size. */
export type CellValue = string | number | null | { $blob: number }

export type RowKey = { rowid?: number; pk?: Record<string, CellValue> }

export type Sort = { column: string; desc: boolean }

export type FilterOp = 'eq' | 'neq' | 'gt' | 'gte' | 'lt' | 'lte' | 'contains' | 'is_null' | 'not_null'

export type Filter = { column: string; op: FilterOp; value: string }

export type RowsResult = {
  columns: string[]
  rows: CellValue[][]
  keys: RowKey[]
  total: number
  editable: boolean
}

export type Mutation = {
  table: string
  kind: 'insert' | 'update' | 'delete'
  values?: Record<string, string | null>
  keys?: RowKey[]
}

export type ColumnDef = {
  name: string
  type: string
  pk?: boolean
  notNull?: boolean
  unique?: boolean
  default?: string | null
}

export type Alter =
  | { kind: 'create_table'; table: string; columns: ColumnDef[] }
  | { kind: 'add_column'; table: string; def: ColumnDef }
  | { kind: 'rename_column'; table: string; column: string; newName: string }
  | { kind: 'drop_column'; table: string; column: string }
  | { kind: 'rename_table'; table: string; newName: string }
  | { kind: 'drop_table'; table: string }

/** A query saved in the SQL Editor. */
export type SavedQuery = { id: string; name: string; sql: string }

export type ExecResult = {
  columns: string[]
  rows: CellValue[][]
  truncated: boolean
  changes: number
  needsWrite: boolean
}

export type MetricPoint = { t: number; cpu: number; mem: number }

/** The server at one moment; rates are per second. */
export type ServerPoint = MetricPoint & {
  iowait: number
  steal: number
  load1: number
  swap: number
  diskRead: number
  diskWrite: number
  netRx: number
  netTx: number
}

export type Mount = { path: string; device: string; fsType: string; totalBytes: number; usedBytes: number; availBytes: number; data: boolean }

export type SystemInfo = {
  host: {
    hostname: string
    os: string
    kernel: string
    arch: string
    cpuModel: string
    cpus: number
    machine: string
    bootedAt?: string
  }
  now: {
    cpuPercent: number
    iowaitPercent: number
    stealPercent: number
    load1: number
    load5: number
    load15: number
    processes: number
    memTotalBytes: number
    memAvailableBytes: number
    memCachedBytes: number
    swapTotalBytes: number
    swapFreeBytes: number
    diskReadBps: number
    diskWriteBps: number
    netRxBps: number
    netTxBps: number
  }
  mounts: Mount[]
  apps: {
    name: string
    state: RuntimeState
    cpuPercent: number
    memoryBytes: number
    memoryMaxBytes: number
    dataBytes: number
    startedAt?: string
  }[]
  sideyard: {
    version: string
    runner: string
    instance: string
    dataDir: string
    startedAt: string
    cpuPercent: number
    memoryBytes: number
  }
}

export type UpdateStatus = {
  current: string
  source: string
  platform: string
  latest?: string
  published?: string
  available: boolean
  checkedAt?: string
  error?: string
  state: 'idle' | 'checking' | 'installing' | 'restarting'
  autoInstall: boolean
  last?: { from: string; to: string; at: string }
  rolledBack?: { from: string; to: string; at: string }
}

export type RemoteBackup = { key: string; size: number; takenAt: string }

export type DomainStatus = {
  host: string
  addresses: string[]
  pointsHere: boolean | null
  dnsError?: string
  certificate: { issuer: string; notBefore: string; notAfter: string } | null
}

export type EmailLog = {
  sender: string
  recipients: string
  subject: string
  status: 'sent' | 'failed'
  detail: string
  createdAt: string
}

export type ProjectEmail = {
  configured: boolean
  sender: string
  defaultSender: string
  senderDomains: string[]
  log: EmailLog[]
}

export type BackupSettings = {
  enabled: boolean
  endpoint: string
  region: string
  bucket: string
  accessKey: string
  secretKey?: string
  hasSecret: boolean
  prefix: string
  intervalHours: number
  retentionDays: number
}

export type EmailSettings = {
  enabled: boolean
  host: string
  port: number
  security: 'starttls' | 'tls' | 'none'
  username: string
  password?: string
  hasPassword: boolean
  from: string
  senderDomains: string[]
}

export type BackupRun = {
  id: number
  trigger: string
  startedAt: string
  finishedAt?: string
  status: 'running' | 'succeeded' | 'partial' | 'failed'
  files: number
  bytes: number
  log: string
}

export type PlatformSettings = {
  backups: BackupSettings
  email: EmailSettings
  server: { domain: string; dashboardHost: string; dataDir: string; runner: string; dev: boolean }
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 401 && path !== '/api/login') {
    window.dispatchEvent(new Event('sideyard:signed-out'))
  }
  const data = res.status === 204 ? null : await res.json().catch(() => null)
  if (!res.ok) {
    throw new ApiError(res.status, data?.error ?? `Request failed (${res.status})`)
  }
  return data as T
}

const p = (name: string) => `/api/projects/${encodeURIComponent(name)}`

export const api = {
  session: () => request<{ authenticated: boolean }>('GET', '/api/session'),
  login: (password: string) => request<{ authenticated: boolean }>('POST', '/api/login', { password }),
  logout: () => request<void>('POST', '/api/logout'),
  server: () => request<ServerInfo>('GET', '/api/server'),

  projects: () => request<Project[]>('GET', '/api/projects'),
  project: (name: string) => request<Project>('GET', p(name)),
  createProject: (name: string) =>
    request<{ project: Project; deployToken: string }>('POST', '/api/projects', { name }),
  deleteProject: (name: string, confirm: string) => request<void>('DELETE', p(name), { confirm }),

  restart: (name: string) => request<void>('POST', `${p(name)}/restart`),
  stop: (name: string) => request<void>('POST', `${p(name)}/stop`),
  rollback: (name: string, number: number) =>
    request<{ release: Release }>('POST', `${p(name)}/releases/${number}/activate`),
  logs: (name: string, lines = 500, q = '') =>
    request<{ lines: string[] }>('GET', `${p(name)}/logs?lines=${lines}${q ? `&q=${encodeURIComponent(q)}` : ''}`),

  variables: (name: string) =>
    request<{ builtins: Builtin[]; variables: { key: string; updatedAt: string }[] }>('GET', `${p(name)}/variables`),
  revealVariable: (name: string, key: string) =>
    request<{ key: string; value: string }>('GET', `${p(name)}/variables/${encodeURIComponent(key)}`),
  updateVariables: (name: string, set: Record<string, string>, del: string[] = []) =>
    request<{ restarted: boolean; error?: string }>('PUT', `${p(name)}/variables`, { set, delete: del }),

  updateSettings: (name: string, settings: Settings) => request<void>('PUT', `${p(name)}/settings`, settings),
  addDomain: (name: string, host: string) => request<void>('POST', `${p(name)}/domains`, { host }),
  removeDomain: (name: string, host: string) =>
    request<void>('DELETE', `${p(name)}/domains/${encodeURIComponent(host)}`),
  regenerateToken: (name: string) => request<{ deployToken: string }>('POST', `${p(name)}/token`),

  databases: (name: string) => request<{ databases: Database[]; backups: Database[] }>('GET', `${p(name)}/databases`),
  databaseDownloadURL: (name: string, path: string) =>
    `${p(name)}/databases/download?path=${encodeURIComponent(path)}`,
  importDatabase: (name: string, path: string, file: File, onProgress: (fraction: number) => void) =>
    upload<{ path: string; backup: string }>(`${p(name)}/databases/import`, { path, database: file }, onProgress),
  schema: (name: string, path: string) =>
    request<TableInfo[]>('GET', `${p(name)}/db/schema?path=${encodeURIComponent(path)}`),
  rows: (
    name: string,
    path: string,
    req: { table: string; limit: number; offset: number; sort: Sort[]; filters: Filter[] },
  ) => request<RowsResult>('POST', `${p(name)}/db/rows`, { path, ...req }),
  edit: (name: string, path: string, m: Mutation) =>
    request<{ affected: number; rowid?: number }>('POST', `${p(name)}/db/edit`, { path, ...m }),
  alter: (name: string, path: string, a: Alter) => request<{ sql: string }>('POST', `${p(name)}/db/alter`, { path, ...a }),
  sql: (name: string, path: string, sql: string, write: boolean) =>
    request<{ result: ExecResult; ms: number }>('POST', `${p(name)}/db/sql`, { path, sql, write }),
  queries: (name: string) => request<SavedQuery[]>('GET', `${p(name)}/queries`),
  saveQuery: (name: string, q: SavedQuery) =>
    request<void>('PUT', `${p(name)}/queries/${encodeURIComponent(q.id)}`, { name: q.name, sql: q.sql }),
  deleteQuery: (name: string, id: string) => request<void>('DELETE', `${p(name)}/queries/${encodeURIComponent(id)}`),
  metrics: (name: string, range = '1h') => request<MetricPoint[]>('GET', `${p(name)}/metrics?range=${range}`),
  serverMetrics: (range = '1h') => request<ServerPoint[]>('GET', `/api/server/metrics?range=${range}`),
  system: () => request<SystemInfo>('GET', '/api/system'),
  update: () => request<UpdateStatus>('GET', '/api/update'),
  checkUpdate: () => request<UpdateStatus>('POST', '/api/update/check'),
  installUpdate: (version: string) => request<UpdateStatus>('POST', '/api/update/install', { version }),
  saveUpdateSettings: (autoInstall: boolean) => request<UpdateStatus>('PUT', '/api/update/settings', { autoInstall }),
  remoteBackups: (name: string, path: string) =>
    request<{ enabled: boolean; backups: RemoteBackup[] }>('GET', `${p(name)}/databases/remote?path=${encodeURIComponent(path)}`),
  remoteBackupURL: (name: string, path: string, key: string) =>
    `${p(name)}/databases/remote/download?path=${encodeURIComponent(path)}&key=${encodeURIComponent(key)}`,
  restoreBackup: (name: string, path: string, key: string) =>
    request<{ path: string; backup: string }>('POST', `${p(name)}/databases/restore`, { path, key }),
  domainStatus: (name: string) => request<DomainStatus[]>('GET', `${p(name)}/domains/status`),

  storage: (name: string) => request<StorageInfo>('GET', `${p(name)}/storage`),
  connectStorage: (name: string, input: StorageInput) =>
    request<{ restarted: boolean; error?: string }>('PUT', `${p(name)}/storage`, input),
  disconnectStorage: (name: string) => request<{ restarted: boolean; error?: string }>('DELETE', `${p(name)}/storage`),
  objects: (name: string, prefix: string, after = '') =>
    request<{ objects: S3Object[]; more: boolean }>(
      'GET',
      `${p(name)}/storage/objects?prefix=${encodeURIComponent(prefix)}&after=${encodeURIComponent(after)}`,
    ),
  objectURL: (name: string, key: string) => `${p(name)}/storage/download?key=${encodeURIComponent(key)}`,
  deleteObject: (name: string, key: string) =>
    request<void>('DELETE', `${p(name)}/storage/objects?key=${encodeURIComponent(key)}`),
  uploadObject: (name: string, prefix: string, file: File, onProgress: (fraction: number) => void) =>
    upload<{ uploaded: string[] }>(`${p(name)}/storage/upload`, { prefix, file }, onProgress),
  storageUsage: (name: string) =>
    request<{ objects: number; bytes: number; truncated: boolean }>('GET', `${p(name)}/storage/usage`),

  jobs: (name: string) => request<Job[]>('GET', `${p(name)}/jobs`),
  createJob: (name: string, spec: JobSpec) => request<void>('POST', `${p(name)}/jobs`, spec),
  updateJob: (name: string, job: string, spec: JobSpec) =>
    request<void>('PUT', `${p(name)}/jobs/${encodeURIComponent(job)}`, spec),
  deleteJob: (name: string, job: string) => request<void>('DELETE', `${p(name)}/jobs/${encodeURIComponent(job)}`),
  runJob: (name: string, job: string) => request<JobRun>('POST', `${p(name)}/jobs/${encodeURIComponent(job)}/run`),
  jobRuns: (name: string, job: string) => request<JobRun[]>('GET', `${p(name)}/jobs/${encodeURIComponent(job)}/runs`),

  projectEmail: (name: string) => request<ProjectEmail>('GET', `${p(name)}/email`),
  setSender: (name: string, sender: string) => request<void>('PUT', `${p(name)}/email`, { sender }),
  projectTestEmail: (name: string, to: string) => request<void>('POST', `${p(name)}/email/test`, { to }),

  settings: () => request<PlatformSettings>('GET', '/api/settings'),
  saveBackups: (s: BackupSettings) => request<void>('PUT', '/api/settings/backups', s),
  runBackup: () => request<BackupRun>('POST', '/api/settings/backups/run'),
  backupRuns: () => request<BackupRun[]>('GET', '/api/settings/backups/runs'),
  saveEmail: (s: EmailSettings) => request<void>('PUT', '/api/settings/email', s),
  testEmail: (to: string) => request<void>('POST', '/api/settings/email/test', { to }),
  changePassword: (current: string, next: string) =>
    request<void>('POST', '/api/settings/password', { current, new: next }),
  masterKey: async (password: string): Promise<Blob> => {
    const res = await fetch('/api/settings/master-key', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password }),
    })
    if (!res.ok) {
      const data = await res.json().catch(() => null)
      throw new ApiError(res.status, data?.error ?? `Request failed (${res.status})`)
    }
    return res.blob()
  },
}

/** A multipart POST with upload progress, which fetch can't report. */
function upload<T>(path: string, fields: Record<string, string | File>, onProgress: (fraction: number) => void): Promise<T> {
  return new Promise((resolve, reject) => {
    const form = new FormData()
    for (const [k, v] of Object.entries(fields)) form.append(k, v)
    const xhr = new XMLHttpRequest()
    xhr.open('POST', path)
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total)
    xhr.onload = () => {
      let data: { error?: string } | null = null
      try {
        data = JSON.parse(xhr.responseText)
      } catch {}
      if (xhr.status >= 200 && xhr.status < 300) return resolve(data as T)
      if (xhr.status === 401) window.dispatchEvent(new Event('sideyard:signed-out'))
      reject(new ApiError(xhr.status, data?.error ?? `Upload failed (${xhr.status})`))
    }
    xhr.onerror = () => reject(new Error('Upload failed: connection lost'))
    xhr.send(form)
  })
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
