# Health Operations API

Health data is an operations and diagnostics surface, not a primary user feature.
The web navbar intentionally does not expose a Health page.

## Purpose

Use these endpoints when operating or debugging routing behavior:

- Inspect channel/key/model health scores.
- Diagnose timeout, rate-limit, network, model, and key-level error patterns.
- Verify adaptive first-token timeout behavior.
- Export health metrics to Prometheus.

## Endpoints

All routes are authenticated under `/api/v1/health`.

- `GET /api/v1/health/status`
  - Returns all in-memory health states.
- `GET /api/v1/health/status/channel?channel_id=<id>`
  - Returns health states for one channel.
- `GET /api/v1/health/status/specific?channel_id=<id>&key_id=<id>&model=<model>`
  - Returns one channel/key/model health state.
- `POST /api/v1/health/reset`
  - Clears in-memory health states.
- `POST /api/v1/health/enable`
  - Enables the health manager.
- `POST /api/v1/health/disable`
  - Disables the health manager.
- `GET /api/v1/health/metrics`
  - Serves Prometheus metrics for health state snapshots.

## UI Policy

Do not expose raw health state as a main navigation item unless the page is
rebuilt as an actionable diagnostics workflow. A user-facing diagnostics page
should show channel names, key remarks, model names, clear problem labels, and
next actions such as checking quota, disabling a key, adjusting timeout policy,
or jumping to the relevant channel/group configuration.
