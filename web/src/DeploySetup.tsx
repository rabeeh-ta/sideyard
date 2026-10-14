import { useState } from 'react'
import { api, errorMessage } from './api'
import { CodeBlock } from './ui'
import { useServer } from './App'

// A new deploy token is only ever returned once. Keep it for this browser tab
// until the user dismisses it, so a reload doesn't lose it.
const key = (name: string) => `sideyard:token:${name}`
export const rememberToken = (name: string, token: string) => sessionStorage.setItem(key(name), token)
const forgetToken = (name: string) => sessionStorage.removeItem(key(name))
const recallToken = (name: string) => sessionStorage.getItem(key(name))

/** How to connect a repo: GitHub secrets for CI, or a curl from a laptop. */
export function DeploySetup({ name, onDone }: { name: string; onDone?: () => void }) {
  const [token, setToken] = useState(() => recallToken(name))
  const [error, setError] = useState<string | null>(null)
  const url = window.location.origin
  const arch = useServer()?.arch ?? 'amd64'

  async function regenerate() {
    setError(null)
    try {
      const { deployToken } = await api.regenerateToken(name)
      rememberToken(name, deployToken)
      setToken(deployToken)
    } catch (e) {
      setError(errorMessage(e))
    }
  }

  const shownToken = token ?? '<your deploy token>'
  return (
    <div className="setup">
      {token ? (
        <div className="notice">
          <strong>Deploy token.</strong> This is the only time it's shown. Add it to your repo now.
          <CodeBlock>{token}</CodeBlock>
        </div>
      ) : (
        <p className="muted">
          The deploy token was shown when it was created.{' '}
          <button className="link-button" onClick={regenerate}>
            Generate a new one
          </button>{' '}
          (the old one stops working).
        </p>
      )}
      {error && <p className="error">{error}</p>}

      <h4>Deploy from GitHub</h4>
      <p className="muted">
        In your app's repo (the starter template includes <code>.github/workflows/deploy.yml</code>), run:
      </p>
      <CodeBlock>{`gh secret set SIDEYARD_TOKEN --body "${shownToken}"
gh variable set SIDEYARD_URL --body "${url}"
gh variable set SIDEYARD_PROJECT --body "${name}"${arch !== 'amd64' ? `
gh variable set SIDEYARD_ARCH --body "${arch}"` : ''}`}</CodeBlock>
      <p className="muted">Then push to main. CI builds the binary and uploads it here.</p>

      <h4>Or from your laptop</h4>
      <CodeBlock>{`CGO_ENABLED=0 GOOS=linux GOARCH=${arch} go build -o app .
curl --fail-with-body -H "Authorization: Bearer ${shownToken}" \\
  -F binary=@app ${url}/api/projects/${name}/deploys`}</CodeBlock>

      {token && onDone && (
        <button
          className="ghost"
          onClick={() => {
            forgetToken(name)
            onDone()
          }}
        >
          I've saved the token
        </button>
      )}
    </div>
  )
}

export function hasRememberedToken(name: string) {
  return recallToken(name) !== null
}
