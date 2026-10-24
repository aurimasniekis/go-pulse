# Metrics

pulsed serves Prometheus metrics on its metrics listener, `PULSE_METRICS_ADDR` (default `127.0.0.1:9464`), published
on host loopback only. See [monitoring and alerts](/operators/monitoring) for how to scrape and use them.

## Endpoints on the metrics listener

| Path | Response |
|---|---|
| `GET /metrics` | Prometheus text format |
| `GET /healthz` | `200 ok`: the process is up |
| `GET /readyz` | `200 ready` once pulsed is serving, `503 not ready` before; used by `pulsed healthcheck` |
| `GET /internal/v1/bans` | The ban feed for banagent; needs its bearer token |

The public listener has its own liveness route, `GET /api/v1/health`.

## pulsed

| Metric | Type | Labels |
|---|---|---|
| `pulse_build_info` | gauge | `version` |
| `pulse_nats_connected` | gauge | — (1 while pulsed's NATS connection is up) |
| `pulse_http_requests_total` | counter | `listener`, `route`, `method`, `code` |
| `pulse_http_request_duration_seconds` | histogram | `listener`, `route` (SSE streams count until they end) |
| `pulse_ingest_events_total` | counter | `via`: `http`, `hook`, `admin` |
| `pulse_rollup_events_total`, `pulse_rollup_bytes_total` | counter | `stream` (every publisher, including raw NATS) |
| `pulse_auth_failures_total` | counter | `source` |
| `pulse_bans_total` | counter | `source`: `nats`, `http_token`, `webhook`, `manual` |
| `pulse_bans_active` | gauge | — |
| `pulse_inbound_hooks_total` | counter | `result`: `accepted`, `rejected`, `error` |
| `pulse_outbound_deliveries_total` | counter | `outcome`: `delivered`, `retry`, `dead`, `manual`, `test` |
| `pulse_alerts_firing` | gauge | `severity` |
| `pulse_alert_transitions_total` | counter | `rule`, `state`: `firing`, `resolved` |
| `pulse_notifications_total` | counter | `notifier`, `result`: `sent`, `failed`, `suppressed` |
| `pulse_certificate_not_after_timestamp_seconds` | gauge | `certificate` |
| `pulse_acme_attempts_total` | counter | `result`: `ok`, `error` |
| `pulse_backups_total` | counter | `result`: `ok`, `failed` |
| `pulse_backup_uploaded_bytes_total` | counter | — (encrypted bytes, after deduplication) |
| `pulse_tail_sessions` | gauge | — |
| `pulse_stream_bytes`, `pulse_stream_max_bytes`, `pulse_stream_messages`, `pulse_stream_consumers` | gauge | `stream` (`max_bytes` is -1 when unlimited) |
| `pulse_stream_first_timestamp_seconds` | gauge | `stream` (the oldest stored message: the real retention horizon) |

`route` is the first path segment only (`/api/admin/v1/clients`, `/api/v1/publish`, `/hooks`, `/ws`, `ui`), never an ID,
name or subject.

## NATS

Collected from the NATS server's VARZ and JSZ at scrape time and cached for 5 seconds.

| Metric | Type |
|---|---|
| `nats_up` | gauge (1 when the server answered) |
| `nats_connections`, `nats_subscriptions` | gauge |
| `nats_connections_total` | counter |
| `nats_in_msgs_total`, `nats_out_msgs_total`, `nats_in_bytes_total`, `nats_out_bytes_total` | counter |
| `nats_slow_consumers_total` | counter |
| `nats_mem_bytes`, `nats_cpu_percent` | gauge |
| `nats_jetstream_storage_bytes`, `nats_jetstream_storage_max_bytes`, `nats_jetstream_memory_bytes` | gauge |
| `nats_jetstream_streams`, `nats_jetstream_consumers`, `nats_jetstream_messages` | gauge |
| `nats_jetstream_api_requests_total`, `nats_jetstream_api_errors_total` | counter |

Plus the standard Go and process collectors (`go_*`, `process_*`).

## Useful queries

```txt
# Events stored per minute, all publishers
sum(rate(pulse_rollup_events_total{stream="EVENTS"}[5m])) * 60

# Days until a certificate expires
(pulse_certificate_not_after_timestamp_seconds - time()) / 86400

# Fill level of EVENTS
pulse_stream_bytes{stream="EVENTS"} / pulse_stream_max_bytes{stream="EVENTS"}

# Public API error rate
sum(rate(pulse_http_requests_total{listener="public",code=~"5.."}[5m]))
```
