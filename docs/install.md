# Installing Sideyard on a server

One command on a fresh Ubuntu or Debian server, amd64 or arm64. A 4 GB / 1 vCPU VM is plenty for a handful of small apps: an idle Go app uses tens of MB of RAM.

## 1. DNS

At your DNS provider, add a wildcard **A** record for your domain pointing at the server's IPv4 address (and an **AAAA** record if it has IPv6):

```
*.example.com   A   203.0.113.10
```

This covers the dashboard (`sideyard.example.com`) and every project's subdomain (`notes.example.com`). Custom domains for individual apps each need their own A record pointing at the same IP.

If the domain is on Cloudflare, set these records to **DNS only** (grey cloud). Sideyard gets its own certificates.

No domain yet? Skip this step: the installer offers `<server IP>.sslip.io` (for 203.0.113.10, `203-0-113-10.sslip.io`), a free name from [sslip.io](https://sslip.io) that resolves to the IP inside it, subdomains included. It follows the server's IP, so it's for trying Sideyard out; [switch to your own domain](#changing-the-domain) when you have one.

## 2. Install

```sh
curl -fsSL https://github.com/rabeeh-ta/sideyard/releases/latest/download/install.sh | sudo bash
```

It asks two things:

- **Domain**: `example.com`. Apps get `<name>.example.com`; the dashboard is `sideyard.example.com`. Press Enter for the sslip.io name instead.
- **Admin password**, twice (12+ characters). Leave it empty and one is generated and shown once.

It then checks that ports 80 and 443 are free, shows the DNS record to add if `sideyard.<domain>` doesn't point at the server yet, offers to open ports 80 and 443 if `ufw` is on, and asks before changing anything. Then it:

| Step | Where |
|---|---|
| Downloads the release for this CPU, checks it against the release's `SHA256SUMS`, and checks that it runs | `/opt/sideyard/bin/sideyard`, linked from `/usr/local/bin/sideyard` |
| Writes the settings (root-only) | `/etc/sideyard/sideyard.env` |
| Writes the service and starts it | `/etc/systemd/system/sideyard.service` |
| Sets the admin password before the first start | stored hashed in `/var/lib/sideyard/sideyard.db` |

It finishes when the dashboard answers, and prints its address. The first visit can take a few seconds while the certificate is issued.

Sideyard runs as root. It creates a Unix user for each project (`sy<instance>-<id>-<name>`, where the instance is a short ID unique to this installation) and runs every release as its own systemd unit. Each unit runs as that user, with a memory limit (512 MB by default) and write access only to the project's data folder. Apps keep running when Sideyard restarts or updates.

```sh
systemctl list-units 'sy-*'            # one unit per running release
journalctl -u sideyard -f              # Sideyard's own log
journalctl SIDEYARD_PROJECT_ID=1 -f     # a project's log (the dashboard's Logs tab)
```

### Without questions

For scripts and cloud-init, pass the answers as variables (after `sudo`, which drops the environment) and `--yes`:

```sh
curl -fsSL https://github.com/rabeeh-ta/sideyard/releases/latest/download/install.sh \
  | sudo SIDEYARD_DOMAIN=example.com bash -s -- --yes
```

`SIDEYARD_ADMIN_PASSWORD` sets the password; without it one is generated and printed. `--version v1.2.3` installs a given release instead of the newest.

## 3. Deploy an app

Sign in, create a project, and follow the "Connect deploys" steps it shows. New apps start from [`templates/starter`](../templates/starter).

To move an existing app with its data, create the project, use **Database → Import** to upload its SQLite file to the path the app expects (for example `data/esphog.db`), then deploy. Make the copy with `sqlite3 app.db ".backup copy.db"` so it's consistent even while the old app runs.

## Updates

Sideyard checks for a new release every six hours. **Platform settings → Updates** shows it, with a button to install it; turn on *Install new releases automatically* to skip the button. An update downloads the new binary, checks its checksum and that it runs, waits for deploys in progress, and restarts Sideyard into it. Apps keep serving throughout; only the dashboard and new connections pause for a second or two.

If a new version fails to start three times in a row, Sideyard puts the previous binary back and starts that; the dashboard says so, and automatic updates skip that version.

Running the install command again does the same from a shell, going back by itself if the new version doesn't stay up. It only moves forward unless you ask: `--version v1.2.3` installs any release, older ones included. The binary an update replaced is kept as `/opt/sideyard/bin/sideyard.prev`; to go back by hand:

```sh
sudo mv /opt/sideyard/bin/sideyard.prev /opt/sideyard/bin/sideyard && sudo systemctl restart sideyard
```

## Settings

`/etc/sideyard/sideyard.env` holds them; restart Sideyard after a change (`sudo systemctl restart sideyard`).

| Variable | Meaning |
|---|---|
| `SIDEYARD_DOMAIN` | Base domain. Projects get `<name>.<domain>`. |
| `SIDEYARD_ACME_EMAIL` | Email for the Let's Encrypt account (optional). Let's Encrypt no longer sends expiry notices or keeps the address, so leaving it empty loses nothing. |
| `SIDEYARD_PUBLIC_IP` | The server's public IPv4, so the dashboard can tell whether a domain points here when the VM is behind NAT (most cloud VMs). The installer fills it in. |
| `SIDEYARD_DASHBOARD_HOST` | Serve the dashboard somewhere other than `sideyard.<domain>`. |
| `SIDEYARD_UPDATE_SOURCE` | Where to look for releases, if not where this build came from (a fork's repository URL), or `off`. |

To set a new admin password: `sudo sideyard reset-password` prints one and signs out every browser.

### Changing the domain

Point the new domain at the server (step 1), set `SIDEYARD_DOMAIN` to it and restart. The dashboard moves to `sideyard.<new domain>` and new projects get `<name>.<new domain>`. Projects you already have keep their addresses: add the new one in each project's **Settings → Domains** and remove the old.

## What to back up

Everything lives in `/var/lib/sideyard`:

| Path | Contents |
|---|---|
| `sideyard.db` | Projects, domains, releases and encrypted variables |
| `master.key` | The key that decrypts variables. Without it, every variable is lost. |
| `projects/<name>/data/` | Each app's SQLite database and persistent files |
| `projects/<name>/data/.sideyard/backups/` | Databases replaced by imports (the last 10) |
| `projects/<name>/releases/` | The last five binaries, for rollback |
| `caddy/` | TLS certificates (reissued automatically if lost) |

App logs live in journald, not in this folder.

Set up off-server backups in **Platform settings → Backups**: connect an S3-compatible bucket (one used only for backups) and Sideyard copies `sideyard.db` and every app's databases to it on a schedule. Then use **Platform settings → Security → Download master key** and keep the key somewhere safe away from the server; it isn't uploaded with the backups.

To rebuild a dead server: install Sideyard, stop it, put your saved `master.key` and the latest `sideyard.db` copy (gunzipped) in `/var/lib/sideyard`, start it, then redeploy each app from CI and restore its database from **Database → Backups in S3**.

## Publishing releases

Releases come from this repository's [release workflow](../.github/workflows/release.yml). Push a version tag and it runs the tests, builds `make release VERSION=<tag> SOURCE=<this repository's URL>`, and publishes the Linux binaries, `release.json` (what the update check reads), `SHA256SUMS` and `install.sh` as a GitHub release:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

Servers download releases without credentials, so the repository must be public. A fork publishes its own releases the same way, and its servers update from the fork. The source must be https: a release's checksums come from the same place as its binaries, so only TLS keeps someone on the network from swapping both.

To try a release before publishing it, build one that looks for releases on the test server itself (`make release VERSION=v0.1.0 SOURCE=http://127.0.0.1:8765`; plain http is accepted only on loopback) and serve `dist/` there, laid out like GitHub Releases: `releases/download/<version>/<file>`, plus `releases/latest/download/` holding the newest.
