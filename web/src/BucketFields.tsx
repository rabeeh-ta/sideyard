import { useState } from 'react'

/** The connection details of an S3-compatible bucket, as the API takes them. */
export type BucketValue = { endpoint: string; region: string; bucket: string; accessKey: string; secretKey?: string }

type Provider = 'aws' | 'r2' | 'b2' | 'spaces' | 'other'

type Spec = {
  label: string
  keyLabel: string
  secretLabel: string
  keyHint: string
  regions?: string[]
  regionHint?: string
}

const providers: Record<Provider, Spec> = {
  aws: {
    label: 'AWS S3',
    keyLabel: 'Access key ID',
    secretLabel: 'Secret access key',
    keyHint: "IAM → a user's Security credentials → Create access key. Limit it to this bucket.",
    regions: [
      'us-east-1', 'us-east-2', 'us-west-1', 'us-west-2', 'ca-central-1', 'sa-east-1', 'eu-west-1', 'eu-west-2', 'eu-west-3',
      'eu-central-1', 'eu-north-1', 'eu-south-1', 'me-central-1', 'af-south-1', 'ap-south-1', 'ap-southeast-1',
      'ap-southeast-2', 'ap-northeast-1', 'ap-northeast-2', 'ap-northeast-3',
    ],
    regionHint: "The bucket's region, shown next to it in the S3 console.",
  },
  r2: {
    label: 'Cloudflare R2',
    keyLabel: 'Access Key ID',
    secretLabel: 'Secret Access Key',
    keyHint: 'R2 → Manage API tokens → Create API token, with Object Read & Write on this bucket.',
  },
  b2: {
    label: 'Backblaze B2',
    keyLabel: 'keyID',
    secretLabel: 'applicationKey',
    keyHint: 'Application Keys → Add a New Application Key, allowed only this bucket.',
    regions: ['us-west-000', 'us-west-001', 'us-west-002', 'us-west-004', 'us-east-005', 'eu-central-003', 'ca-east-006'],
    regionHint: "From the bucket's S3 endpoint: s3.us-west-004.backblazeb2.com is us-west-004.",
  },
  spaces: {
    label: 'DigitalOcean Spaces',
    keyLabel: 'Access Key',
    secretLabel: 'Secret Key',
    keyHint: 'Spaces Object Storage → Access Keys → Create Access Key, limited to this bucket.',
    regions: ['nyc3', 'sfo2', 'sfo3', 'ams3', 'fra1', 'lon1', 'sgp1', 'blr1', 'syd1', 'tor1', 'atl1'],
    regionHint: "The datacenter in the Space's URL: nyc3, fra1 and so on.",
  },
  other: {
    label: 'Other',
    keyLabel: 'Access key ID',
    secretLabel: 'Secret access key',
    keyHint: 'A key that can read, write, list and delete in this bucket only.',
  },
}

const order: Provider[] = ['aws', 'r2', 'b2', 'spaces', 'other']

function host(endpoint: string) {
  const e = endpoint.trim().toLowerCase()
  if (!e) return ''
  try {
    return new URL(e.includes('://') ? e : `https://${e}`).hostname
  } catch {
    return ''
  }
}

/** detect works out the provider of a saved bucket from its endpoint. */
function detect(v: BucketValue): { provider: Provider; account: string } {
  const h = host(v.endpoint)
  if (!v.endpoint.trim() || h.endsWith('.amazonaws.com')) return { provider: 'aws', account: '' }
  const r2 = h.match(/^([a-z0-9]+)\.r2\.cloudflarestorage\.com$/)
  if (r2) return { provider: 'r2', account: r2[1] }
  if (/^s3\.[a-z0-9-]+\.backblazeb2\.com$/.test(h)) return { provider: 'b2', account: '' }
  if (/^[a-z0-9-]+\.digitaloceanspaces\.com$/.test(h)) return { provider: 'spaces', account: '' }
  return { provider: 'other', account: '' }
}

/** location is the endpoint and region a provider's fields come to. */
function location(p: Provider, account: string, region: string, v: BucketValue) {
  region = region.trim().toLowerCase()
  switch (p) {
    case 'aws':
      // Empty: Sideyard makes https://s3.<region>.amazonaws.com.
      return { endpoint: '', region }
    case 'r2': {
      const id = accountID(account)
      return { endpoint: id ? `https://${id}.r2.cloudflarestorage.com` : '', region: 'auto' }
    }
    case 'b2':
      return { endpoint: region ? `https://s3.${region}.backblazeb2.com` : '', region }
    case 'spaces':
      return { endpoint: region ? `https://${region}.digitaloceanspaces.com` : '', region }
    case 'other':
      return { endpoint: v.endpoint, region: v.region }
  }
}

/** An R2 account ID, from the ID itself or a pasted endpoint. */
function accountID(s: string) {
  const t = s.trim().toLowerCase()
  return t.match(/([a-z0-9]+)\.r2\.cloudflarestorage\.com/)?.[1] ?? t.replace(/[^a-z0-9]/g, '')
}

/**
 * BucketFields asks for a bucket the way its provider names things: pick
 * the provider, then only the fields it needs. The endpoint and region the
 * API stores are worked out from them. The secret is write-only: when one
 * is saved, leaving the field empty keeps it.
 */
export function BucketFields({
  value,
  onChange,
  secretSaved,
  required,
}: {
  value: BucketValue
  onChange: (v: BucketValue) => void
  secretSaved: boolean
  required?: boolean
}) {
  const [initial] = useState(() => detect(value))
  const [provider, setProvider] = useState<Provider>(initial.provider)
  const [account, setAccount] = useState(initial.account) // as typed; accountID reads it
  const spec = providers[provider]

  const update = (patch: Partial<BucketValue>, p = provider, acct = account) => {
    const next = { ...value, ...patch }
    onChange({ ...next, ...location(p, acct, next.region, next) })
  }

  function choose(p: Provider) {
    if (p === provider) return
    setProvider(p)
    if (p === 'other') {
      // Start from where the last choice pointed, to edit from there.
      const endpoint = provider === 'aws' && value.region ? `https://s3.${value.region}.amazonaws.com` : value.endpoint
      onChange({ ...value, endpoint })
      return
    }
    // Region names don't carry over between providers.
    update({ region: p === 'aws' ? 'us-east-1' : '' }, p)
  }

  const derived = provider === 'aws' ? (value.region ? `https://s3.${value.region}.amazonaws.com` : '') : value.endpoint
  const regionList = `regions-${provider}`

  return (
    <div className="stack bucket-fields">
      <div className="field">
        <span>Provider</span>
        <div className="chips" role="radiogroup" aria-label="Provider">
          {order.map((p) => (
            <button
              type="button"
              key={p}
              role="radio"
              aria-checked={provider === p}
              className={`chip ${provider === p ? 'on' : ''}`}
              onClick={() => choose(p)}
            >
              {providers[p].label}
            </button>
          ))}
        </div>
      </div>
      <div className="grid2">
        {provider === 'other' && (
          <label className="field span2">
            <span>Endpoint URL</span>
            <input
              value={value.endpoint}
              onChange={(e) => update({ endpoint: e.target.value })}
              placeholder="https://s3.example.com"
              required={required}
              spellCheck={false}
            />
            <small className="muted">MinIO, Wasabi, Hetzner, Garage or any S3-compatible service: its S3 endpoint.</small>
          </label>
        )}
        {provider === 'r2' ? (
          <label className="field">
            <span>Account ID</span>
            <input
              value={account}
              onChange={(e) => {
                setAccount(e.target.value)
                update({}, provider, e.target.value)
              }}
              placeholder="32 letters and digits"
              required={required}
              spellCheck={false}
            />
            <small className="muted">On the R2 overview page of the Cloudflare dashboard.</small>
          </label>
        ) : (
          <label className="field">
            <span>Region{provider === 'other' && ' (optional)'}</span>
            <input
              value={value.region}
              onChange={(e) => update({ region: e.target.value })}
              list={spec.regions ? regionList : undefined}
              placeholder={spec.regions?.[0] ?? 'us-east-1'}
              required={required && provider !== 'other'}
              spellCheck={false}
            />
            {spec.regions && (
              <datalist id={regionList}>
                {spec.regions.map((r) => (
                  <option key={r} value={r} />
                ))}
              </datalist>
            )}
            {spec.regionHint && <small className="muted">{spec.regionHint}</small>}
          </label>
        )}
        <label className="field">
          <span>Bucket name</span>
          <input value={value.bucket} onChange={(e) => update({ bucket: e.target.value })} required={required} spellCheck={false} />
        </label>
        {provider !== 'other' && derived && (
          <small className="muted span2 derived">
            Endpoint <code>{derived}</code>
          </small>
        )}
        <label className="field">
          <span>{spec.keyLabel}</span>
          <input
            value={value.accessKey}
            onChange={(e) => update({ accessKey: e.target.value })}
            required={required}
            spellCheck={false}
            autoComplete="off"
          />
        </label>
        <label className="field">
          <span>{spec.secretLabel}</span>
          <input
            type="password"
            value={value.secretKey ?? ''}
            onChange={(e) => update({ secretKey: e.target.value })}
            required={required && !secretSaved}
            placeholder={secretSaved ? 'saved; leave empty to keep it' : ''}
            autoComplete="new-password"
          />
        </label>
        <small className="muted span2">{spec.keyHint}</small>
      </div>
    </div>
  )
}
