# Health and Observability Runtime Surface

This document records the runtime surface that is implemented and registered by the current server.

## Public routes

| Method and path | Behavior |
|---|---|
| `GET /health` | Checks the database and returns HTTP 503 when the service is degraded. |
| `GET /ready` | Readiness check backed by the database. |
| `GET /readiness` | Compatibility alias for `/ready`. |
| `GET /liveness` | Process liveness check without a database probe. |
| Application listener `GET /metrics` | Always 404; metrics are never exposed on the application listener. |
| Dedicated metrics listener `GET /metrics` | Prometheus endpoint when enabled; optional IP/CIDR allowlist and Bearer authentication, which is mandatory for non-loopback binding. |

There are no registered `/api/v1/health/*` routes. In particular, health-state listing, reset, enable, disable, and a separate health metrics handler are not supported HTTP operations.

## Internal channel health

The relay maintains channel/key/model health state for adaptive first-token timeouts and health-aware routing. It persists snapshots under `data/health` using the current built-in policy. The main configuration schema does not accept a `health` section or a separate `health.yaml` file.

Operators can use the health-related settings already exposed by the management settings API. Raw in-memory state is intentionally not exposed until a complete, authenticated operations API is implemented.

## Security boundary

Health/readiness/liveness routes share the main service listener and expose only coarse state; database error details remain server-side. Metrics use a separate listener configured by `observability.metrics.host`, `port`, `bearer_token`, and optional `allowlist`. Allowlist entries are exact IP addresses or CIDRs and are checked against the direct TCP peer before Bearer authentication; forwarded headers are deliberately ignored. The default is disabled and loopback-only (`127.0.0.1:9090`). A non-loopback metrics host without a token is rejected during configuration validation; network policy or a firewall should still be used as a second boundary.

Example:

```json
{
  "observability": {
    "metrics": {
      "enabled": true,
      "host": "0.0.0.0",
      "port": 9090,
      "bearer_token": "REPLACE_ME_REPLACE_ME",
      "allowlist": ["10.0.0.0/8", "2001:db8:1234::/48"]
    }
  }
}
```

Any future operations API must be added as an authenticated Gin route, use stable response contracts, protect state-changing operations, and include route/auth/config tests before its documentation is published.
