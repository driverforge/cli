# Instrumenting a driver

What the Driverforge SDK records on its own, and how to add the evidence a question
needs. Full SDK documentation: https://go.driverforge.com/sdk. For anything this
skill doesn't cover, the documentation index for agents is
https://docs.driverforge.com/llms.txt: it links every page as Markdown.

## Setting up a driver

A driver without `.driverforge/config.json` isn't set up yet. `driverforge init`
embeds the SDK, wires `Driverforge:Init` and `Anvil:OnDriverInit` into the entry
file, and links or creates the Driverforge project the driver reports to. Use it
rather than writing that wiring by hand.

Under a coding agent init can't ask its questions, so every answer is a flag. Run
`driverforge init` with none first: its `input_required` error lists what's
unanswered in `details.missing`, and what it found in the driver (the logger
global, the versioning scheme) in `details.detected`.

1. Ask the developer whether to link an existing project
   (`driverforge init --project <org>/<slug>`) or create one
   (`driverforge init --create-project <name>`, with `--org <slug>` when no
   organization is active). Creating a project counts against the organization's
   plan: only create one when the developer asks for it.
2. Answer the rest: `--versioning date|integer` (the detected scheme unless told
   otherwise), `--build-configs` or `--no-build-configs`, `--logger <global>`
   (the detected logger) or `--no-logger`, and in a git repository `--branch
   <name>` or `--no-branch`.
3. Add `--dry-run`. Show the developer the `project` it will link or create and
   each `changes[].diff`. Nothing is created or written.
4. Once they approve, run the same command with `--yes` instead. The result
   carries the `project` (`action`, `id`, `slug`) and the files it wrote.
5. `driverforge doctor` reports nothing missing.

init runs once. On a driver that's already set up it reports `already_set_up` and
changes nothing; `driverforge doctor` repairs missing wiring and
`driverforge upgrade` updates the SDK.

## What is recorded without code changes

Once the driver calls `Driverforge:Init`, the SDK wraps the Control4 callbacks the
driver defines and records each call as an **event**: its `name`, `args` (named
after the handler's parameters), `result`, `status` and `duration_ms`. That covers
`ExecuteCommand`, `ReceivedFromProxy`, `ReceivedFromNetwork`, `ReceivedFromSerial`,
`OnPropertyChanged`, `OnTimerExpired`, `OnBindingChanged`, `OnVariableChanged`,
`OnConnectionStatusChanged`, `UIRequest`, `OnDriverLateInit` and the rest of the
Control4 entry points. Read them with `driverforge events --name <callback>`.

A handler is wrapped only if the driver defines it **before** it requires the SDK.
A handler defined afterwards replaces the wrapper and goes dark.

An error a handler throws is recorded as an **error** with its stack trace, the
handler's arguments and the last log line before it, then re-raised unchanged.

## Logs

Logs reach Driverforge when the driver passes its logger to the SDK:

```lua
Driverforge:Init("drv_…", { logger = Log })
```

Every call to that logger's `Trace`, `Debug`, `Info`, `Warn`, `Error` and `Fatal`
methods (any of lower, Pascal or UPPER case) is then forwarded as well as written
as before. Without a logger, forward explicitly with `Anvil:ForwardLog("INFO", ...)`,
which takes the same arguments as a logger call.

Pass tables straight to the logger; don't encode them:

```lua
Log:Info("vendor-raw status", response)
```

The log's `message` reads `vendor-raw status [table]`, and the arguments arrive
in order, as JSON, in its `payload.args`:

```json
"payload": {"args": ["vendor-raw status", {"status": 200, "body": "OK"}], "argsTruncated": false}
```

A log whose arguments were all strings has a null `payload`. Values JSON can't
hold arrive as placeholders: `"[nil]"`, `"[function]"`, `"[userdata]"`,
`"[cycle]"`, `"nan"`, `"inf"`. The SDK caps each call's arguments (depth 6,
100 entries per table, 2 KiB per string, about 16 KiB in all). A cut leaves
`"[truncated]"` in place and sets `argsTruncated: true`. For a bigger
response, log just the part you need, or log it in pieces.

If `message` shows `table: 0x…` and `payload` is null, the driver's vendored
SDK predates structured arguments. Update it, or encode the table yourself:
`Log:Info("vendor-raw status", C4:JsonEncode(response))`.

Give temporary lines a prefix you can wait on (`vendor-raw`, `dbg-`), and remove
them or lower them to `Debug` when the job is done.

## Correlation

A log or error raised while a callback runs carries that callback's trace id as its
`correlation_id`, and the event's `id` is the same trace id. So
`driverforge logs --correlation-id <event id>` is everything that callback logged.

Work that runs later — a `C4:url` or `C4:urlGet` response callback, a timer — is
outside the callback and gets a fresh trace id. Link it yourself by logging an
identifier at both ends, and capture its errors explicitly:

```lua
Anvil:SetTimer(5000, function() Poll() end)            -- errors in the callback are recorded
Anvil:CaptureError("parse failed: " .. tostring(err))  -- anywhere else
```

## Startup

OnDriverInit runs before the SDK can send anything. Wrap the work in
`Anvil:OnDriverInit` so it is recorded and relayed once the SDK is ready:

```lua
function OnDriverInit(strDIR)
    require('vendor.driverforge-sdk')
    Driverforge:Init("drv_…", { logger = Log })
    Anvil:OnDriverInit(function(strDIR)
        LoadDefaults()
    end, strDIR)
end
```

OnDriverLateInit is wrapped like any other callback.

## Checking the SDK is live

`Anvil:TelemetryStatus()` returns `{ enabled, state, reason }`, where `state` is
`initialising`, `ready`, `verifying`, `no-agent` or `disabled`. Log it from
OnDriverLateInit when telemetry is missing and the Control4 log is the only
other place to look. `driverforge doctor` checks the SDK is wired into the driver.
