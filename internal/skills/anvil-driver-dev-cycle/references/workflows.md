# Workflows

One section per job. Each runs the core loop in SKILL.md with a different question.
Commands assume a driver repo with a device resolved; add `--device` when not.
`<n>` is the `version` your last `driverforge deploy` returned.

## Discover vendor API behaviour

**Question.** What does this poorly documented API actually return?

1. Find where the response arrives. Data from a network or serial binding arrives
   as a callback (`ReceivedFromNetwork`, `ReceivedFromSerial`) and is already in
   that event's `payload.args`, so read it first:
   `driverforge events --version <n> --name ReceivedFromNetwork --tail 5`.
   An HTTP response arrives in a callback the driver passes to `C4:url` or
   `C4:urlGet`, which isn't recorded on its own: log the raw body there, before
   parsing, with a recognisable prefix (see instrumenting.md).
2. `driverforge deploy`, then trigger the request.
3. `driverforge logs --version <n> --until-match 'vendor-raw' --timeout 30s`.
4. Read `entries[].message`. If an entry is truncated, `driverforge get <id>`.
5. Compare the real structure with what the parser expects: wrappers, field names,
   types (strings vs numbers), arrays of one, nulls, error shapes.

**Done when** the parser handles the real structure, the parse runs without errors
on the new version, and the raw-body log is removed or lowered to `debug`.

## Discover runtime behaviour

**Question.** Which callbacks does the Control4 runtime call, with what arguments,
in what order, after a UI action?

The SDK records every callback the driver defines as an event, with its arguments
and result. No code change is needed unless the question is about what happens
*inside* the handler.

1. Record a marker and use the handshake in triggering.md.
2. `driverforge events --version <n> --since <marker> --min-events 1 --timeout 60s`.
   When you expect several callbacks, wait for the last one you expect with
   `--until-match`, or raise `--min-events`.
3. Read the entries oldest first: `name`, `payload.args`, `payload.result`,
   `status`, `duration_ms`. Note callbacks you didn't expect — the runtime often
   sends more than one.
4. For what the handler did, `driverforge logs --correlation-id <event id>`.

**Done when** the sequence is understood and the handler is written to it.

## Verify a change

**Question.** Did my edit do what I intended?

1. Before editing, note the `version` the last deploy returned. Call it `<prev>`.
2. Edit, `driverforge deploy`, note the new `version`: `<n>`.
3. Repeat the same trigger.
4. Read both versions with the same filters:
   `driverforge logs --version <prev> --grep '<pattern>' --tail 50 > prev.json` and
   `driverforge logs --version <n> --grep '<pattern>' --tail 50 > new.json`.
   Do the same with `errors`.
5. Compare: the expected entries now appear, the unwanted ones don't, and no new
   errors appear.

**Done when** the expected entries appear and nothing regressed.

## Diagnose a failure

**Question.** Why did this error occur?

If the developer quotes a reference such as `POOL-PUMP-4F2A9C1B`, start with
`driverforge get <ref>`: it reads the whole group, however old, and its
`latest_occurrence.command` fetches the newest occurrence for step 2.

1. `driverforge errors --version <n> --tail 10`.
2. Read `message`, `payload.type`, `payload.stacktrace`, `payload.args` and
   `payload.lastLog` (the last log line before it was raised). For its history,
   `driverforge get <ref>` with the entry's `ref`: how often it has occurred,
   since when, and on which versions and devices. Quote the `ref` to the
   developer; it's the name the web app shows.
3. Pull the surrounding trail: `driverforge logs --correlation-id <correlation_id>`
   and `driverforge events --correlation-id <correlation_id>`.
4. Form the cause from that evidence, fix it, redeploy, repeat the trigger.
5. `driverforge errors --version <n>` must now be empty for that error.

**Done when** the cause is fixed and the error no longer appears on the new version.

## Validate before shipping

**Question.** Does the driver behave across its scenarios with no new errors?

Write each scenario as a trigger plus a bounded read that fails loudly:

```sh
set -e
export DRIVERFORGE_OUTPUT=json   # CI isn't a coding agent; ask for JSON
driverforge deploy > deploy.json
jq -e '.state == "confirmed"' deploy.json
V=$(jq -r '.version' deploy.json)
# scenario: startup initialises defaults
driverforge logs --version "$V" --until-match 'defaults loaded' --timeout 60s > /dev/null
# no errors on this version
test "$(driverforge errors --version "$V" | jq '.count')" -eq 0
```

- A missing expected entry exits 3 and fails the script.
- In CI, set scope explicitly (`DRIVERFORGE_ORG`, `DRIVERFORGE_PROJECT`,
  `DRIVERFORGE_DEVICE`) against a test bench, and always read the version the
  pipeline's own deploy returned.
- Only driver-initiated triggers (startup, timers, polling) run unattended today.

**Done when** the checks pass locally and in CI against a test bench.

## Understand timing and order

**Question.** What happens in what order, and how long does it take?

1. Read events over the window with `driverforge events --version <n> --since <marker>`.
2. Order by `timestamp`; `duration_ms` is each callback's own run time.
3. Group logs and errors under their callback with `correlation_id`.
4. For latency between a request and its response, log both ends and subtract
   timestamps.

**Done when** the order and latency are known.

## Verify startup behaviour

**Question.** What does the driver do when it loads?

Deploying reloads the driver, so the deploy is the trigger.

1. `driverforge deploy`. Check `state`: `confirmed_with_errors` means init threw.
2. `driverforge events --version <n> --name OnDriverLateInit --min-events 1 --timeout 60s`.
   OnDriverInit runs before the SDK is ready; it is recorded only when the driver
   wraps its work in `Anvil:OnDriverInit` (see instrumenting.md).
3. `driverforge logs --correlation-id <the event's id>` for what it did, and
   `driverforge errors --version <n>`.

`driverforge sync` does not re-run OnDriverInit or OnDriverLateInit. Use `deploy`.

**Done when** startup does what is intended and raises no errors.
