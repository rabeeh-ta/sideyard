# Agents over MCP

Sideyard serves an MCP endpoint, so an agent such as Claude Code, Codex or Cursor can create projects, deploy, read logs and iterate without the dashboard. Set it up once: make an access token on the dashboard's **MCP** page and add it to the agent. The page has the setup for Claude Code, Codex, Cursor, VS Code and Gemini CLI, and JSON for the rest. For Claude Code it's:

```
claude mcp add --scope user --transport http sideyard https://sideyard.example.com/mcp \
  --header "Authorization: Bearer sy_mcp_…"
```

## How it fits

- **Same binary, same platform.** `/mcp` is served by the API server on the dashboard host (`internal/api/mcp.go`, tools in `mcptools.go`), with the official Go SDK (`github.com/modelcontextprotocol/go-sdk`, v1). Tools call the `platform` methods the dashboard's handlers call, so deploy safety, project locks and validation are shared, not copied.
- **Stateless streamable HTTP** with JSON responses. Every request stands alone, so a Sideyard restart or self-update never breaks an agent's connection, and nothing is held in memory per client.
- **Behind the proxy.** The API listens on loopback, so the SDK's localhost DNS-rebinding check is off (every request arrives from 127.0.0.1 with the dashboard's Host). Only bearer tokens authenticate `/mcp`, never cookies, so neither rebinding nor CSRF applies.

## Access tokens

- Table `access_tokens` (migration 5): name, SHA-256 of the token, access level, created and last-used times. The token (`sy_mcp_…`, 256 random bits) is shown once at creation. Revoking deletes the row and takes effect on the next request.
- Tokens don't expire. A solo admin revokes them on the MCP page, which shows when each was last used. Expiry would mean re-adding the server to every agent on a schedule, for little gain.
- **Levels**, each including the ones before it. A level decides which tools *exist* for the token: Sideyard builds one MCP server per level and picks it per request, so an agent never sees a tool it can't call.

| Level | Tools |
|---|---|
| `read` | `list_projects`, `get_project`, `get_logs`, `list_variables` (names only), `check_domains`, `list_jobs`, `get_job_runs`, `query_database` (read-only SQL) |
| `manage` (default) | also `create_project`, `deploy`, `restart`, `stop`, `rollback`, `set_variables`, `update_settings`, `add_domain`, `remove_domain`, `save_job`, `delete_job`, `run_job`, `new_deploy_token` |
| `full` | also `delete_project` |

Not available over MCP at any level: revealing a variable's value, the admin password, the master key, Sideyard updates, platform settings (backups, email), database import, restore or writes, the storage browser and bucket credentials, and token management itself.

## Deploys

A deploy is a binary upload built on the agent's machine (`POST /api/projects/<name>/deploys`, multipart), which can't travel as tool arguments. The `deploy` tool issues a **one-time upload ticket** (`sy_once_…`, valid 15 minutes, for one project) and returns the build command for the server's OS and architecture plus a `curl` upload command. The agent runs them; the upload returns the release when it's live (200) or failed (422), as it does for CI. The deploy endpoint accepts a ticket in place of the deploy token and uses it up on the first attempt. Tickets live in memory, so a restart only means asking for another.

For deploys on every push, `new_deploy_token` replaces the project's deploy token and returns the `gh secret`/`gh variable` commands the starter's workflow needs. That token does enter the agent's context, which is why `create_project` doesn't return one.

## Safety

- **Untrusted content is marked.** Log lines, job output and query rows are wrapped in a tag with a random id, such as `<app-logs id="k3J9xQ2a">…</app-logs id="k3J9xQ2a">`, after a sentence telling the agent this is data and not instructions. The random id stops content from closing the tag early. The server's MCP instructions say the same.
- **Secrets stay write-only.** Variables can be set but never read back. The call log keeps `set_variables` keys and drops their values.
- **Call log.** Every tool call is recorded (`mcp_calls`, newest 1,000): token, tool, project, other arguments truncated to 1 KB, and the error if it failed, including calls to tools the token's level doesn't have. The MCP page shows the last 100. The log keeps the token's name, so it outlives a revoked token.
- **Destructive tools** carry MCP's `destructiveHint` (`stop`, `rollback`, `remove_domain`, `delete_job`, `new_deploy_token`, `delete_project`), so clients ask before running them. `delete_project` also needs `confirm` set to the project's name, as the dashboard does. `stop` and `rollback` have no confirm argument: both are undone with one call, and a flag the model fills in itself adds a round trip without adding a check.
- **Rate limits.** None of their own. Operations on one project are already serialised by its lock, the same as for dashboard requests; an agent makes one call at a time; and a misbehaving token shows up in the call log and is revoked there.

## Domains

`add_domain` adds the domain and returns the record to create: a CNAME to the dashboard host for a subdomain, or A/AAAA records for the server's public addresses (`SIDEYARD_PUBLIC_IP`, else what the dashboard host resolves to) for an apex. In both cases it's DNS only, not proxied, since Sideyard gets the certificate itself. `check_domains` reports whether each domain points here and which certificate Sideyard holds for it.

## Not yet

- **OAuth.** claude.ai's custom connectors and other clients that can't send a static header need OAuth 2.1 with dynamic client registration. Sideyard would become its own authorization server, approving clients on a dashboard page. The SDK has the protected-resource metadata pieces.
- Metrics history, storage, email and backup tools. `get_project` already includes current memory, CPU and health.
