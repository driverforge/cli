# Commands

Written for driverforge `{{cli_version}}`. `driverforge <command> --help` is the
authority if anything here disagrees.

## logs, errors, events

`driverforge logs`, `driverforge errors` and `driverforge events` share one flag set
and one output shape. They differ in the entries they read and a few filters.

### Scope

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--org` | `DRIVERFORGE_ORG` | Organization slug, name or id |
| `--project` | `DRIVERFORGE_PROJECT` | Project slug, name or id |
| `--device` | `DRIVERFORGE_DEVICE` | Device name, hostname, hardware id or id |
| `--version` | | `current` (this machine's last deploy to the device), `latest` (the highest version in the device's telemetry) or a version number |

Precedence is flag, then environment, then the repo's `.driverforge/config.json`
(org and project), then the selected org and device. The device falls back to the
org's only device; with several and none selected the read fails with
`context_ambiguous` rather than choosing. Nothing prompts.

`--version current` is the version `driverforge deploy` last recorded for this
project and device on this machine. `driverforge sync` doesn't record one, so it
still names the last deploy.

### Window and paging

| Flag | Meaning |
| --- | --- |
| `--since` | Start: a duration back from now (`30s`, `10m`, `2h`) or an RFC 3339 time |
| `--until` | End, in the same forms |
| `--tail` | Newest N entries, returned oldest first. Default 100, max 1000 |
| `--before` | Page backwards: pass `next_cursor` from the previous response |

### Filters

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--grep` | all | Go RE2 regular expression over the message and the serialised payload |
| `-i`, `--ignore-case` | all | Make `--grep` case-insensitive |
| `--correlation-id` | all | Only entries from one callback invocation (its trace id) |
| `--level` | logs, errors | Comma-separated levels: `trace`, `debug`, `info`, `warn`, `error`, `fatal` |
| `--name` | events | The callback name, such as `ExecuteCommand` |
| `--field key=value` | all, repeatable | Facets. logs: `controller`, `driver`, `level`. errors: `controller`, `driver`. events: `controller`, `driver`, `name`, `status` |

### Waiting and streaming

| Flag | Meaning |
| --- | --- |
| `--until-match` | Wait until an entry matching this regular expression arrives |
| `--min-events` | Wait until this many entries pass the filters |
| `--timeout` | Longest wait. Default 30s when a stop condition is set; max 10m |
| `-f`, `--follow` | Stream entries as they arrive, until interrupted or `--timeout` |

A wait first reads what already exists in the window, then watches live entries. It
returns as soon as the stop condition holds, so a generous `--timeout` costs nothing
when the entry arrives quickly. Not met in time: exit 3.

With `--follow`, stdout carries one JSON entry per line, and the scope, warnings and
wait outcome arrive as JSON lines on stderr. Always bound it with `--timeout`.

### Output

| Flag | Meaning |
| --- | --- |
| `-o`, `--output` | `text`, `json` or `jsonl`. Defaults to JSON under a coding agent; `jsonl` when following |
| `--max-payload-bytes` | Cap each entry's message or payload. Default 65536; 0 for no cap |

## get

`driverforge get <id>` fetches one entry with its full payload. A log or error id is
a UUID; an event's id is its 32-character trace id, which is also the
`correlation_id` of the logs and errors raised inside it.

| Flag | Meaning |
| --- | --- |
| `--type` | `log`, `error` or `event`, to skip the lookup |
| `--org`, `--project` | Scope, as above. `get` reads the whole project, so it takes no device |
| `-o`, `--output` | `text` or `json` |
| `--max-payload-bytes` | Cap the payload. Default 0: no cap |

The `payload_truncated` warning lists a ready-to-run `get` command for each entry
it cut.

## Around the loop

| Command | Use |
| --- | --- |
| `driverforge deploy` | Build and install the driver with a new version, reloading it. The result carries `version` and `state` |
| `driverforge sync` | Hot-swap the Lua without a reload. Same version; OnDriverInit and OnDriverLateInit don't re-run |
| `driverforge device list` | The org's controllers |
| `driverforge device select` | Choose the default controller. Interactive: ask the developer to run it, or pass `--device` instead |
| `driverforge device capabilities` | Check the Anvil agent on the controller answers |
| `driverforge doctor` | Check the driver's SDK wiring |
| `driverforge whoami` | Check the sign-in |
