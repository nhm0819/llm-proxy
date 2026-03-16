# LLM Proxy — Environment Variable Reference

All configuration is loaded from environment variables at startup.
If a required variable is missing a safe default is used (shown below).

## Upstream Routing

| Variable | Default | Description |
|----------|---------|-------------|
| `UPSTREAM_BASE_URL` | `https://api.openai.com` | Base URL for the default upstream (used when `ROUTES_JSON` is absent) |
| `UPSTREAM_API_KEY` | _(empty)_ | API key injected into every upstream request |
| `ROUTES_JSON` | _(empty)_ | JSON array of `RouteRule` objects for multi-backend routing (see below) |

### ROUTES_JSON format
```json
[
  {"prefix": "gpt",    "name": "openai",    "base_url": "https://api.openai.com",    "api_key": "sk-..."},
  {"prefix": "claude", "name": "anthropic", "base_url": "https://api.anthropic.com", "api_key": "sk-ant-..."},
  {"prefix": "",       "name": "default",   "base_url": "https://api.openai.com",    "api_key": "sk-..."}
]
```
The rule with the **longest matching prefix** wins. An empty prefix is the fallback default.

---

## Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `PROXY_API_KEYS_JSON` | _(empty)_ | JSON object mapping proxy API keys to user IDs. **Empty = no auth (dev only).** |

```json
{"sk-proxy-abc123": "alice", "sk-proxy-def456": "bob"}
```
Clients must send `Authorization: Bearer <proxy-key>`. The proxy strips this header and injects the upstream API key before forwarding.

---

## Token Quota (per user, per day)

| Variable | Default | Description |
|----------|---------|-------------|
| `TOKEN_DAILY_LIMIT` | `200000` | Default daily token budget per user |
| `USER_TOKEN_LIMITS_JSON` | `{}` | Per-user overrides: `{"alice": 500000, "bob": 50000}` |
| `TOKEN_SAFETY_FACTOR` | `1.20` | Multiplier applied to prompt token estimate when reserving quota |
| `DEFAULT_MAX_COMPLETION_TOKENS_CHAT` | `1024` | Used in reserve calculation when `max_tokens` is absent from request |
| `DEFAULT_MAX_OUTPUT_TOKENS_RESPONSES` | `1024` | Same, for Responses API |
| `QUOTA_TIMEZONE` | `Asia/Seoul` | Timezone for daily quota reset (midnight in this timezone) |

---

## Rate Limiting (RPS per user)

| Variable | Default | Description |
|----------|---------|-------------|
| `RATE_LIMIT_RPS` | `5` | Default requests per second per user |
| `USER_RATE_LIMITS_JSON` | `{}` | Per-user overrides: `{"alice": 20, "bob": 2}` |

---

## PII Detection

| Variable | Default | Description |
|----------|---------|-------------|
| `PII_BLOCK` | `false` | `true` = reject requests containing PII with HTTP 400; `false` = detect + log only |

Built-in patterns: Korean RRN (with checksum validation), email addresses, Korean mobile numbers.

---

## Redis

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost:6379` | Redis server address |
| `REDIS_PASSWORD` | _(empty)_ | Redis AUTH password |
| `REDIS_DB` | `0` | Redis logical database index |

---

## Audit Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `AUDIT_STREAM_KEY` | `audit:llm-proxy` | Redis Stream key for audit records |
| `AUDIT_RECORD_TTL_SECONDS` | `604800` (7 days) | TTL for per-request hash entries |
| `AUDIT_STORE_REQUEST_RECORD` | `true` | Store per-request hash detail in Redis |
| `AUDIT_HMAC_KEY` | _(empty)_ | If set, use HMAC-SHA256 instead of plain SHA-256 for content hashes |

---

## Observability: Loki

| Variable | Default | Description |
|----------|---------|-------------|
| `LOKI_PUSH_URL` | _(empty)_ | Loki push API URL e.g. `http://loki:3100/loki/api/v1/push`. Empty = disabled. |
| `LOKI_LABELS` | _(empty)_ | Comma-separated `key=value` pairs added to every Loki stream |

---

## Proxy Behaviour

| Variable | Default | Description |
|----------|---------|-------------|
| `UPSTREAM_TIMEOUT_SECONDS` | `300` | Total timeout for upstream HTTP requests (covers streaming) |
| `INJECT_CHAT_STREAM_USAGE` | `false` | Auto-inject `stream_options.include_usage=true` for chat streaming |
| `INJECT_CHAT_STREAM_USAGE_OPENAI_ONLY` | `true` | Only inject usage for `api.openai.com` when the above is enabled |

---

## Prometheus Metrics

Exposed at `GET /metrics` (no authentication).

| Metric | Type | Labels |
|--------|------|--------|
| `llm_proxy_requests_total` | Counter | status, kind, upstream, user_id |
| `llm_proxy_request_duration_seconds` | Histogram | kind, upstream |
| `llm_proxy_tokens_used_total` | Counter | user_id, kind, model |
| `llm_proxy_quota_rejected_total` | Counter | user_id |
| `llm_proxy_rps_rejected_total` | Counter | user_id |
| `llm_proxy_pii_detected_total` | Counter | kind, pii_type, direction |
| `llm_proxy_circuit_state` | Gauge | upstream (0=closed, 1=open, 2=half-open) |

---

## Response Headers

| Header | Description |
|--------|-------------|
| `X-Request-ID` | Unique request ID (echoed from client or generated) |
| `X-Token-Limit` | User's daily token budget |
| `X-Token-Used` | Tokens used so far today (updated after each call) |
| `X-Token-Remaining` | Tokens remaining today |
