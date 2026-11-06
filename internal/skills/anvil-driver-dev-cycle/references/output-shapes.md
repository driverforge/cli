# Output shapes

Written for driverforge `{{cli_version}}`. Codes and field names are a stable
contract; messages are not. Branch on codes.

## Response envelope

`logs`, `errors` and `events` write one document to stdout:

```json
{
  "scope": {
    "org": "acme",
    "project": "smart-tv",
    "device": {"id": "000FFF9BB74C", "name": "bench-a"},
    "version": "2026110401",
    "since": "2026-11-05T09:00:00Z",
    "until": null
  },
  "entries": [],
  "count": 0,
  "has_more": false,
  "next_cursor": null,
  "warnings": [{"code": "no_entries", "message": "…"}],
  "wait": {"satisfied": true, "reason": "until_match", "elapsed_ms": 4210, "transport": "subscription"}
}
```

- `scope.version` is null when the read wasn't scoped to a version.
- `has_more` and `next_cursor`: older entries exist; pass `--before <next_cursor>`.
- `wait` is present only when the read waited. `reason` is `until_match`,
  `min_events` or `timeout`; `transport` is `subscription` or `polling`.

`get` writes `{"scope", "entry", "warnings"}`.

## Entry

Every key is always present, null when it doesn't apply.

| Field | Logs | Errors | Events |
| --- | --- | --- | --- |
| `id` | UUID | UUID | 32-hex trace id |
| `type` | `log` | `error` | `event` |
| `timestamp` | When raised | When raised | When the callback started |
| `level` | The log level | `error` | null |
| `name` | The callback it was raised in | The callback it was raised in | The callback |
| `message` | The log text | The error message | null |
| `payload` | null | `{"type", "stacktrace", "args", "lastLog"}` | `{"args", "result"}` |
| `status` | null | null | The callback's outcome |
| `duration_ms` | null | null | How long the callback ran |
| `error_id` | null | null | The error it raised, if any |
| `driver` | The `.c4z` filename | | |
| `driver_version` | The driver.xml version that produced it | | |
| `device` | `{"id", "name"}`: the controller's hardware id and display name | | |
| `correlation_id` | The trace id shared by a callback and the logs and errors raised inside it | | |
| `truncated` | null, or `{"field", "total_bytes", "returned_bytes"}` | | |

Payloads are passed through exactly as stored: nested JSON stays nested, strings
stay strings.

## Warnings

| Code | Meaning |
| --- | --- |
| `no_entries` | Nothing matched the scope and filters |
| `payload_truncated` | Entries were cut; `details.entries[]` holds each `id` and the `command` that fetches it whole |
| `retention_clamped` | `--since` reached past the plan's retention; `details.window_minutes` is what was searched |
| `tail_clamped` | `--tail` was over 1000 |

With `--follow`, warnings arrive on stderr as `{"warning": {…}}` lines.

## Errors

A failure writes `{"error": {"code", "message", "hint", "details", "reference"}}`
to stdout and the same as a line on stderr.

| Code | Exit | Meaning |
| --- | --- | --- |
| `usage_error` | 2 | A bad flag or value |
| `input_required` | 2 | A command needed an answer a prompt would ask for; `hint` names the flag |
| `auth_required` | 4 | Not signed in, or the session expired |
| `context_unresolved` | 5 | No org, project or device could be determined; `details.missing` names which |
| `context_ambiguous` | 5 | More than one org, project or device could be meant; `details.field` and `details.candidates` |
| `project_not_found` | 5 | No accessible project matches `--project` |
| `device_not_found` | 5 | No device in the org matches `--device` |
| `rate_limited` | 6 | Too many requests; `details.retry_after_ms` when known |
| `entry_not_found` | 1 | `get` found no entry with that id in the project |
| `connection_lost` | 1 | Driverforge couldn't be reached, or a live stream dropped |
| `service_unavailable` | 1 | Driverforge answered with a temporary error |
| `internal_error` | 1 | A bug; `reference` identifies the report |
| `interrupted` | 130 | Interrupted |

A wait that times out is not an error document: stdout still carries the envelope
with `wait.satisfied: false`, stderr carries `{"error": {"code": "wait_timeout"}}`,
and the exit status is 3.

## Exit statuses

| Exit | Meaning |
| --- | --- |
| 0 | Success, including an empty result |
| 1 | Failure |
| 2 | Usage |
| 3 | A wait's stop condition wasn't met before `--timeout` |
| 4 | Not signed in |
| 5 | Scope unresolved |
| 6 | Rate limited |
| 130 | Interrupted |

## deploy

`driverforge deploy` writes `{"action", "reason", "build", "device", "state",
"version", "report_url", "warnings"}`. `state` is `confirmed`,
`confirmed_with_errors` (the driver came up but its init threw) or `unconfirmed`
(no ready signal from the driver in time). Failures include `build_failed`,
`agent_unreachable`, `deploy_failed` and `deploy_timed_out`.
