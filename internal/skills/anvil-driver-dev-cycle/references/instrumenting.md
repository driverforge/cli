# Instrumenting a driver

What the Driverforge SDK records on its own, and how to add the evidence a question
needs. Full SDK documentation: https://go.driverforge.com/sdk.

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
as before. Without a logger, forward explicitly with `Anvil:ForwardLog("INFO", message)`.

A log's arguments are converted with `tostring` and joined with spaces, so a
table logs as `table: 0x…`. Encode structured data first:

```lua
Log:Info("vendor-raw status", C4:JsonEncode(response))
```

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
