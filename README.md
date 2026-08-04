<div align="center">

<img src="web/public/logo.svg" alt="Octopus Logo" width="120" height="120">

### Octopus

**A Simple, Beautiful, and Elegant LLM API Aggregation & Load Balancing Service for Individuals**

 English | [简体中文](README_zh.md)

</div>


## ✨ Features

- 🔀 **Multi-Channel Aggregation** - Connect multiple LLM provider channels with unified management
- 🔑 **Multi-Key Support** - Support multiple API keys for a single channel
- ⚡ **Smart Selection** - Multiple endpoints per channel, smart selection of the endpoint with the shortest delay
- ⚖️ **Load Balancing** - Automatic request distribution for stable and efficient service
- 🔄 **Protocol Conversion** - Seamless conversion between OpenAI Chat / OpenAI Responses / Anthropic API formats
- 💰 **Price Sync** - Automatic model pricing updates
- 🔃 **Model Sync** - Automatic synchronization of available model lists with channels
- 📊 **Analytics** - Comprehensive request statistics, token consumption, and cost tracking
- 🎨 **Elegant UI** - Clean and beautiful web management panel
- 🗄️ **Multi-Database Support** - Support for SQLite, MySQL, PostgreSQL


## 🚀 Quick Start

### 🐳 Docker

Run directly:

```bash
docker volume create octopus-data
docker run -d --name octopus --restart unless-stopped \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
  --security-opt no-new-privileges:true --cap-drop ALL \
  --cpus 1.0 --memory 512m --pids-limit 256 \
  -v octopus-data:/app/data -p 8080:8080 \
  ghcr.io/linbmv/octopus:latest
```

The image runs as UID/GID `10001:10001` and keeps only `/app/data` writable. A
named volume is recommended. For a bind mount, prepare it once with
`install -d -m 0700 ./data && sudo chown -R 10001:10001 ./data`, then replace
the volume argument with `-v "$(pwd)/data:/app/data"`. Tune or remove the sample
CPU, memory, and process limits to match the workload. Data created by an older
root-running image also needs this one-time ownership migration before upgrade;
the container deliberately does not recursively `chown` it on every start.

**Image channels**: `latest` / `latest-alpine` and `vX.Y.Z` come from the formal
tag release pipeline (Cosign-signed with SBOM attestations). `edge` /
`edge-alpine` and `dev-<sha>` are the development channel — rebuilt whenever the
dev branch passes the Quality Gate. They carry the newest changes but are
unsigned; use them for previews and regression testing, not production.

Or use docker compose:

```bash
wget https://raw.githubusercontent.com/linbmv/octopus/refs/heads/dev/docker-compose.yml
docker compose up -d
```


### 📦 Download from Release

Download the binary for your platform from [Releases](https://github.com/linbmv/octopus/releases), then run:

```bash
./octopus start
```

### 🛠️ Build from Source

**Requirements:**
- Go 1.26.5+
- Node.js 22.12+ (Node.js 24.x is used in CI)
- pnpm 9.15.9

```bash
# Clone the repository
git clone https://github.com/linbmv/octopus.git
cd octopus
# Build frontend
cd web && pnpm install --frozen-lockfile && pnpm run build && cd ..
# Move frontend assets to static directory
mv web/out static/
# Start the backend service
go run main.go start 
```

> 💡 **Tip**: The frontend build artifacts are embedded into the Go binary, so you must build the frontend before starting the backend.

**Development Mode**

```bash
cd web && pnpm install --frozen-lockfile && NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm run dev
## Open a new terminal, start the backend service
go run main.go start
## Access the frontend at
http://localhost:3000
```

### 🔐 Initial Administrator

On the first launch of an empty database, Octopus creates one administrator:

- **Username**: `admin`
- **Password (recommended for automated deployments)**: set `OCTOPUS_INITIAL_ADMIN_PASSWORD` to a valid 8–72 byte UTF-8 value before the first launch. The value is never logged and no credential file is created.
- **Password (automatic fallback)**: if that variable is absent, Octopus generates a 24-character random password and writes it to `data/initial-admin-password`. Override the location with `OCTOPUS_INITIAL_ADMIN_PASSWORD_FILE` when the data directory is mounted elsewhere.

The generated credential file is a regular `0600` file; startup rejects symlinks, non-regular files, broader permissions, and invalid password contents. Logs contain only its path, never the password. A secure file left by an interrupted bootstrap is reused on restart, and the tracked file is removed only after the required password change succeeds. For containers, read it from the mounted data directory or run `docker exec octopus cat /app/data/initial-admin-password` (adjust the in-container path for your image).

There is no `admin` default password. Until the password is changed, the account is restricted to the status and password-change endpoints. The existing 403 response helper already aborts the Gin chain; the branch now also calls `c.Abort()` explicitly for defensive consistency and readability, and a regression test locks in the handler-not-called behavior. After a successful change, remove `OCTOPUS_INITIAL_ADMIN_PASSWORD` from deployment configuration so a future empty-database bootstrap cannot reuse it.

### ⚠️ Deployment and Backup Security Boundaries

Octopus currently targets a **single application instance**. The scheduler,
sticky routing, circuit-breaker and health state, runtime caches, and request
rate/concurrency limits are process-local. Multiple replicas sharing one
database can run scheduled jobs more than once and make inconsistent routing or
limit decisions. Run one replica unless you have added external coordination,
shared runtime state, and scheduler leader election.

Database exports include channel credentials and managed Octopus API keys; if
relay logs are included, they can also contain retained request/response
content. Exports remain plaintext JSON by default, while an optional password
header produces a versioned scrypt + AES-256-GCM envelope. See the
[backup encryption guide](docs/backup-encryption.md) for the header, format,
memory limit, and import procedure. Treat every plaintext or encrypted export
as a secret, and rotate credentials after any exposure. Current payload version
2 supports dry-run and UUID-based incremental `reject`/`skip`/`replace`/`merge`;
legacy payload version 1 remains an empty-target restore format.

On Go 1.26.5, the current candidate has passed the full repository race suite,
`go vet`, Go build/module verification, golangci-lint 2.12.2 with 0 issues,
Staticcheck 2026.1, deadcode 0.48.0, govulncheck 1.6.0, actionlint 1.7.12,
script syntax checks, frontend frozen install/lint/type-check, 15 Vitest files
with 40 tests, locale parity, and a Next.js 16.2.10 production build. The
Playwright core flow passes 1/1 and covers bootstrap, forced password change,
Cookie/CSRF, channel, group, API key, model list, relay, logs, and logout.

Real SQLite, MySQL, and PostgreSQL migration/CRUD/export/restore matrices have
passed. A complete release build produced eight Linux/Windows/macOS archives
whose `SHA256SUMS` verify strictly. Alpine and Distroless Debian images passed
arm64 and amd64 non-root/read-only/named-volume/bind-mount/legacy-volume/
Compose/SIGTERM matrices. Current source and all four images have zero
HIGH/CRITICAL Trivy findings; the working tree has zero Gitleaks findings.

These are local candidate results, not a replacement for a clean tagged build.
Known credentials must still be revoked; the six historical Git findings were
formally dispositioned on 2026-07-17 (upstream pre-fork doc-example key,
precise fingerprint risk acceptance in .gitleaksignore, full-history
fail-closed scan passing). The real tag workflow must still produce
and independently verify its Cosign/OIDC signatures, SBOM attestations, and
provenance before production publication. See the
[full audit report](docs/project-audit-2026-07-15.md) for exact evidence and
remaining boundaries.

**Backend dependency security:** the reachable findings in Go 1.26.4,
quic-go 0.57.1, `x/net` 0.52.0, and pgx/v5 5.6.0 were remediated by moving to
Go 1.26.5, quic-go 0.59.1, `x/net` 0.55.0, and pgx/v5 5.9.2; `x/crypto` is
0.52.0 and edwards25519 is 1.1.1. The final govulncheck reports zero reachable
symbol vulnerabilities and zero imported-package vulnerabilities. It still
lists GO-2026-5932 at module level for the `openpgp` package in the required
`x/crypto` module, but Octopus does not import that package or call the
vulnerable code. It is therefore a documented non-applicable module notice,
not a reachable finding.

The final `deadcode -test ./...` scan also produces no output. The remaining
unreferenced internal compatibility/dead-API findings were reviewed and removed; none had a
route, interface, reflection, or build-tag consumer. Related op/helper,
handler, health, relay, and server race suites remain green.

**Frontend dependency security:** the pnpm 9 lockfile overrides vulnerable
transitive versions to `lodash/lodash-es 4.18.1`, `js-cookie 3.0.8`,
`cosmiconfig>yaml 1.10.3`, `mermaid 11.16.0`,
`mermaid>dompurify 3.4.11`, `mermaid>uuid 11.1.1`,
`@lobehub/ui>uuid 13.0.1`, and `next>postcss 8.5.10`; `next-intl` is pinned to
`4.9.2`. Because the legacy audit endpoint used by pnpm 9/10 returns HTTP 410
in 2026, the same final package and lock files were copied to `/tmp` and read
with pnpm 11.13.0 solely for auditing. `audit --prod` exited 0 with “No known
vulnerabilities found,” confirming that the previously reported 3 high,
23 moderate, and 3 low findings were remediated. The project still builds with
pnpm 9.15.9.

Next.js 16.2.10 is the current pinned Next release but itself pins an older
PostCSS, so the `next>postcss` override must be reviewed whenever Next is
upgraded. If the project formally migrates to pnpm 11, move the overrides to
`pnpm-workspace.yaml` before regenerating the lockfile. Recharts v2 deprecation
and React 19 peer warnings remain compatibility debt, not known audit findings.

### 📝 Configuration File

The configuration file is located at `data/config.json` by default and is automatically generated on first startup.

**Complete Configuration Example:**

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8080,
    "trusted_proxies": [],
    "session_cookie_secure": "auto"
  },
  "database": {
    "type": "sqlite",
    "path": "data/data.db"
  },
  "log": {
    "level": "info",
    "format": "json"
  },
  "relay": {
    "initial_response_timeout_seconds": 120,
    "non_stream_timeout_seconds": 120,
    "stream_first_event_timeout_seconds": 120,
    "stream_idle_timeout_seconds": 600,
    "non_stream_attempt_timeout_seconds": 60,
    "stream_cold_start_first_event_timeout_seconds": 30,
    "stream_first_event_budget_seconds": 120,
    "max_json_request_bytes": 33554432,
    "max_image_request_bytes": 67108864,
    "max_non_stream_response_bytes": 67108864
  },
  "observability": {
    "metrics": {
      "enabled": true,
      "host": "127.0.0.1",
      "port": 9090,
      "bearer_token": ""
    },
    "tracing": {
      "enabled": false,
      "endpoint": "localhost:4318",
      "insecure": true,
      "sample_ratio": 0.01
    }
  }
}
```

**Configuration Options:**

| Option | Description | Default |
|--------|-------------|---------|
| `server.host` | Listen address | `0.0.0.0` |
| `server.port` | Server port | `8080` |
| `server.trusted_proxies` | Explicit trusted reverse-proxy IP/CIDR list used for client-IP headers | `[]` |
| `server.session_cookie_secure` | Administrator session cookie policy: `auto` detects direct TLS or trusted-proxy HTTPS; `always` forces `Secure` | `auto` |
| `database.type` | Database type | `sqlite` |
| `database.path` | Database connection string | `data/data.db` |
| `log.level` | Log level | `info` |
| `log.format` | Log encoder (`json` or `console`) | `json` |
| `relay.initial_response_timeout_seconds` | Non-disableable hard ceiling for the complete non-streaming lifecycle across retries and the cumulative pre-first-event streaming phase; range 1–120 seconds | `120` |
| `relay.non_stream_timeout_seconds` | Configured complete non-streaming budget across retries (`0` uses the hard initial-response ceiling; max 86400 seconds). The effective value cannot exceed `initial_response_timeout_seconds`. | `120` |
| `relay.stream_first_event_timeout_seconds` | Per-attempt global fallback from upstream request start through the first non-empty stream event (`0` disables this layer; max 86400 seconds). A group manual timeout wins, followed by an adaptive timeout with samples; cumulative waiting remains hard-bounded. | `120` |
| `relay.stream_idle_timeout_seconds` | Stream idle guard armed only after the first non-empty event and reset by every upstream event or raw heartbeat (`0` disables; max 86400 seconds) | `600` |
| `relay.non_stream_attempt_timeout_seconds` | Per-attempt response-header timeout while another failover candidate remains. The last candidate receives the remaining initial-response budget (`0` disables this layer; max 86400 seconds). | `60` |
| `relay.stream_cold_start_first_event_timeout_seconds` | First-event timeout for a streaming candidate without adaptive samples while another failover candidate remains (`0` disables this layer; max 86400 seconds) | `30` |
| `relay.stream_first_event_budget_seconds` | Cumulative first-event wait budget across streaming channel, key, and endpoint attempts (`0` uses the hard initial-response ceiling). The effective value cannot exceed `initial_response_timeout_seconds`. | `120` |
| `relay.max_json_request_bytes` | Maximum decoded JSON request body; valid range 1 byte–1 GiB | `33554432` (32 MiB) |
| `relay.max_image_request_bytes` | Maximum complete image edit/variation multipart body; valid range 1 byte–1 GiB | `67108864` (64 MiB) |
| `relay.max_non_stream_response_bytes` | Maximum decoded upstream non-streaming response and streaming HTTP error body; successful streams have no total byte cap | `67108864` (64 MiB) |
| `observability.metrics.enabled` | Serve Prometheus metrics on the dedicated metrics listener | `false` |
| `observability.metrics.host` | Dedicated metrics bind address | `127.0.0.1` |
| `observability.metrics.port` | Dedicated metrics port | `9090` |
| `observability.metrics.bearer_token` | Optional Bearer token (required for non-loopback binding; at least 16 bytes) | empty |
| `observability.tracing.enabled` | Export OpenTelemetry traces over OTLP/HTTP | `false` |
| `observability.tracing.endpoint` | OTLP/HTTP collector endpoint | `localhost:4318` |
| `observability.tracing.sample_ratio` | Trace sampling ratio from 0 to 1 | `0.01` |

The configuration file is watched for changes after startup. Valid updates are parsed and validated before the active snapshot is atomically replaced. `log.level`, relay timeouts/body limits, and tracing settings take effect for new requests/attempts without restarting. Changes to the application listener, trusted proxies, session-cookie policy, dedicated metrics listener (including enablement and token), database connection, or log format require a process restart. `/metrics` is never exposed on the application port; scrape `observability.metrics.host:port/metrics` instead.

Relay request bodies with `Content-Encoding` other than empty/`identity` are rejected with 415 before decompression, preventing compressed-body expansion attacks. Oversized JSON or image multipart requests return a structured 413. The non-stream response cap applies after Go transport decompression. Model discovery additionally allows at most 16 MiB per page, 64 MiB cumulatively across keys/pages, and 50,000 unique models; the price catalog has a 16 MiB response cap. Stream first-event and idle guards are phase limits, not a total stream lifetime: active heartbeat-producing streams may run indefinitely, and a recognized terminal event remains successful.

The bundled management UI explicitly logs in with `auth_mode: "cookie"`. It receives an opaque `HttpOnly`, `SameSite=Strict`, host-only session cookie and a separate session-bound CSRF cookie; it never receives, stores, or persists an administrator JWT. Every unsafe cookie-authenticated request must echo the CSRF cookie in `X-Octopus-CSRF`. Browser management is intentionally same-origin, while configured CORS origins remain available to Bearer/API-key clients without cross-origin cookies. In `auto` mode, `X-Forwarded-Proto: https` is accepted only when the request's immediate peer matches `server.trusted_proxies`; use `always` if the deployment cannot reliably provide that header.

For compatibility, non-browser clients that omit `auth_mode` from `POST /api/v1/user/login` continue to receive a Bearer JWT; they may also request `auth_mode: "bearer"` explicitly. Login responses are marked `Cache-Control: no-store`. Browser sessions are kept in the single-instance process, capped at 4096 entries, removed lazily after expiry, and invalidated on restart. `POST /api/v1/user/logout` revokes only the current browser session; changing the administrator password or username increments `token_version` and invalidates all browser sessions and Bearer JWTs.

**Database Configuration:**

Three database types are supported:

| Type | `database.type` | `database.path` Format |
|------|-----------------|-----------------------|
| SQLite | `sqlite` | `data/data.db` |
| MySQL | `mysql` | `user:password@tcp(host:port)/dbname` |
| PostgreSQL | `postgres` | `postgresql://user:password@host:port/dbname?sslmode=disable` |

**MySQL Configuration Example:**

```json
{
  "database": {
    "type": "mysql",
    "path": "root:password@tcp(127.0.0.1:3306)/octopus"
  }
}
```

**PostgreSQL Configuration Example:**

```json
{
  "database": {
    "type": "postgres",
    "path": "postgresql://user:password@localhost:5432/octopus?sslmode=disable"
  }
}
```

> 💡 **Tip**: MySQL and PostgreSQL require manual database creation. The application will automatically create the table structure.

### 🌐 Environment Variables

All configuration options can be overridden via environment variables using the format `OCTOPUS_` + configuration path (joined with `_`):

| Environment Variable | Configuration Option |
|---------------------|---------------------|
| `OCTOPUS_SERVER_PORT` | `server.port` |
| `OCTOPUS_SERVER_HOST` | `server.host` |
| `OCTOPUS_DATABASE_TYPE` | `database.type` |
| `OCTOPUS_DATABASE_PATH` | `database.path` |
| `OCTOPUS_LOG_LEVEL` | `log.level` |
| `OCTOPUS_RELAY_INITIAL_RESPONSE_TIMEOUT_SECONDS` | `relay.initial_response_timeout_seconds` |
| `OCTOPUS_RELAY_NON_STREAM_TIMEOUT_SECONDS` | `relay.non_stream_timeout_seconds` |
| `OCTOPUS_RELAY_STREAM_FIRST_EVENT_TIMEOUT_SECONDS` | `relay.stream_first_event_timeout_seconds` |
| `OCTOPUS_RELAY_STREAM_IDLE_TIMEOUT_SECONDS` | `relay.stream_idle_timeout_seconds` |
| `OCTOPUS_RELAY_MAX_JSON_REQUEST_BYTES` | `relay.max_json_request_bytes` |
| `OCTOPUS_RELAY_MAX_IMAGE_REQUEST_BYTES` | `relay.max_image_request_bytes` |
| `OCTOPUS_RELAY_MAX_NON_STREAM_RESPONSE_BYTES` | `relay.max_non_stream_response_bytes` |
| `OCTOPUS_OBSERVABILITY_METRICS_ENABLED` | `observability.metrics.enabled` |
| `OCTOPUS_OBSERVABILITY_METRICS_HOST` | `observability.metrics.host` |
| `OCTOPUS_OBSERVABILITY_METRICS_PORT` | `observability.metrics.port` |
| `OCTOPUS_OBSERVABILITY_METRICS_BEARER_TOKEN` | `observability.metrics.bearer_token` |

## 📸 Screenshots

### 🖥️ Desktop

<div align="center">
<table>
<tr>
<td align="center"><b>Dashboard</b></td>
<td align="center"><b>Channel Management</b></td>
<td align="center"><b>Group Management</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/desktop-home.png" alt="Dashboard" width="400"></td>
<td><img src="web/public/screenshot/desktop-channel.png" alt="Channel" width="400"></td>
<td><img src="web/public/screenshot/desktop-group.png" alt="Group" width="400"></td>
</tr>
<tr>
<td align="center"><b>Price Management</b></td>
<td align="center"><b>Logs</b></td>
<td align="center"><b>Settings</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/desktop-price.png" alt="Price Management" width="400"></td>
<td><img src="web/public/screenshot/desktop-log.png" alt="Logs" width="400"></td>
<td><img src="web/public/screenshot/desktop-setting.png" alt="Settings" width="400"></td>
</tr>
</table>
</div>

### 📱 Mobile

<div align="center">
<table>
<tr>
<td align="center"><b>Home</b></td>
<td align="center"><b>Channel</b></td>
<td align="center"><b>Group</b></td>
<td align="center"><b>Price</b></td>
<td align="center"><b>Logs</b></td>
<td align="center"><b>Settings</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/mobile-home.png" alt="Mobile Home" width="140"></td>
<td><img src="web/public/screenshot/mobile-channel.png" alt="Mobile Channel" width="140"></td>
<td><img src="web/public/screenshot/mobile-group.png" alt="Mobile Group" width="140"></td>
<td><img src="web/public/screenshot/mobile-price.png" alt="Mobile Price" width="140"></td>
<td><img src="web/public/screenshot/mobile-log.png" alt="Mobile Logs" width="140"></td>
<td><img src="web/public/screenshot/mobile-setting.png" alt="Mobile Settings" width="140"></td>
</tr>
</table>
</div>


## 📖 Documentation

### 📡 Channel Management

Channels are the basic configuration units for connecting to LLM providers.

**Base URL Guide:**

The program automatically appends API paths based on channel type. You only need to provide the base URL:

| Channel Type | Auto-appended Path | Base URL | Full Request URL Example |
|--------------|-------------------|----------|--------------------------|
| OpenAI Chat | `/chat/completions` | `https://api.openai.com/v1` | `https://api.openai.com/v1/chat/completions` |
| OpenAI Responses | `/responses` | `https://api.openai.com/v1` | `https://api.openai.com/v1/responses` |
| Codex OAuth | `/responses` | `https://chatgpt.com/backend-api/codex` (fixed) | `https://chatgpt.com/backend-api/codex/responses` |
| OpenAI Images | `/images/generations`, `/images/edits`, `/images/variations` | `https://api.openai.com/v1` | `https://api.openai.com/v1/images/generations` |
| Anthropic | `/messages` | `https://api.anthropic.com/v1` | `https://api.anthropic.com/v1/messages` |
| Gemini | `/models/:model:generateContent` | `https://generativelanguage.googleapis.com/v1beta` | `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent` |

> 💡 **Tip**: No need to include specific API endpoint paths in the Base URL - the program handles this automatically.

A Codex OAuth channel uses a complete Codex credential JSON document rather
than a normal OpenAI API key. Select **Codex OAuth**, import a local JSON file
or paste the full document into **Codex OAuth JSON**, and keep the fixed
official Base URL. Octopus refreshes tokens before expiry and writes rotations
back atomically. See the
[Codex OAuth channel guide](docs/codex-oauth-channel.md) for credential and
attachment request examples.

---

### 📁 Group Management

Groups aggregate multiple channels into a unified external model name.

**Core Concepts:**

- **Group name** is the model name exposed by the program
- When calling the API, set the `model` parameter to the group name

**Load Balancing Modes:**

| Mode | Description |
|------|-------------|
| 🔄 **Round Robin** | Cycles through channels sequentially for each request |
| 🎲 **Random** | Randomly selects an available channel for each request |
| 🛡️ **Failover** | Prioritizes high-priority channels, switches to lower priority only on failure |
| ⚖️ **Weighted** | Distributes requests based on configured channel weights |

> 💡 **Example**: Create a group named `gpt-4o`, add multiple providers' GPT-4o channels to it, then access all channels via a unified `model: gpt-4o`.

---

### 💰 Price Management

Manage model pricing information in the system.

**Data Sources:**

- The system periodically syncs model pricing data from [models.dev](https://github.com/sst/models.dev)
- When creating a channel, if the channel contains models not in models.dev, the system automatically creates pricing information for those models on this page, so this page displays models that haven't had their prices fetched from upstream, allowing users to set prices manually
- Manual creation of models that exist in models.dev is also supported for custom pricing

**Price Priority:**

| Priority | Source | Description |
|:--------:|--------|-------------|
| 🥇 High | This Page | Prices set by user in price management page |
| 🥈 Low | models.dev | Auto-synced default prices |

> 💡 **Tip**: To override a model's default price, simply set a custom price for it in the price management page.

---

### ⚙️ Settings

Global system configuration.

**Relay log content policy:**

- `metadata` (default): store diagnostics and metadata without request/response bodies.
- `full`: store redacted, size-limited request/response bodies. Redaction reduces risk but cannot guarantee that arbitrary prompt text contains no secrets.
- `disabled`: do not create new Relay logs.

Use `metadata` unless body-level troubleshooting is explicitly required. Changing
the policy does not remove content already persisted or copied into an export.

**Statistics Save Interval (minutes):**

Since the program handles numerous statistics, writing to the database on every request would impact read/write performance. The program uses this strategy:

- Statistics are first stored in **memory**
- Periodically **batch-written** to the database at the configured interval

> ⚠️ **Important**: When exiting the program, use proper shutdown methods (like `Ctrl+C` or sending `SIGTERM` signal) to ensure in-memory statistics are correctly written to the database. **Do NOT use `kill -9` or other forced termination methods**, as this may result in statistics data loss.

---

## 🔌 Client Integration

### OpenAI SDK

```python
from openai import OpenAI
import os

client = OpenAI(   
    base_url="http://127.0.0.1:8080/v1",   
    api_key=os.environ["OCTOPUS_API_KEY"],
)
completion = client.chat.completions.create(
    model="octopus-openai",  # Use the correct group name
    messages = [
        {"role": "user", "content": "Hello"},
    ],
)
print(completion.choices[0].message.content)
```

### Claude Code

Edit `~/.claude/settings.json`

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080",
    "ANTHROPIC_AUTH_TOKEN": "<YOUR_OCTOPUS_API_KEY>",
    "API_TIMEOUT_MS": "3000000",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "ANTHROPIC_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_SMALL_FAST_MODEL": "octopus-haiku-4-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "octopus-haiku-4-5"
  }
}
```

### Codex

Edit `~/.codex/config.toml`

```toml
model = "octopus-codex" # Use the correct group name

model_provider = "octopus"

[model_providers.octopus]
name = "octopus"
base_url = "http://127.0.0.1:8080/v1"
```

Edit `~/.codex/auth.json`

```json
{
  "OPENAI_API_KEY": "<YOUR_OCTOPUS_API_KEY>"
}
```

---

## 🤝 Acknowledgments

- 🙏 [looplj/axonhub](https://github.com/looplj/axonhub) - The LLM API adaptation module in this project is directly derived from this repository
- 📊 [sst/models.dev](https://github.com/sst/models.dev) - AI model database providing model pricing data
- 🇨🇳 [AtomGit](https://atomgit.com/bestruirui/octopus) - China-based code hosting
