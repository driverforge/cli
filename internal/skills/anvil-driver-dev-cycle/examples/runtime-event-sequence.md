# Example: what the runtime sends after a button press

**Situation.** The developer wants the driver to handle the Volume Up button on a
media room's remote. Nobody is sure which callback the runtime calls or with what
arguments.

**1. Decide.** Callbacks are recorded as events with their arguments, so no code
change is needed. The driver is already deployed; `driverforge logs --version current --tail 1`
shows `scope.version` is the deployed version.

**2. Marker.**

```sh
date -u +%Y-%m-%dT%H:%M:%SZ   # 2026-11-05T10:14:03Z
```

**3. Ask.** "In the Control4 app, open the Living Room, select this TV and press
Volume Up once. I'm watching for the callbacks now and will read them myself."

**4. Wait once.**

```sh
driverforge events --version current --since 2026-11-05T10:14:03Z \
  --min-events 1 --timeout 60s \
  | jq '.entries[] | {timestamp, name, args: .payload.args, status, duration_ms}'
```

```json
{"timestamp": "2026-11-05T10:14:21.402Z", "name": "ReceivedFromProxy",
 "args": {"idBinding": 5001, "strCommand": "PULSE_VOL_UP", "tParams": {"ROOM_ID": "12"}},
 "status": "ok", "duration_ms": 0.4}
```

A single event arrived, so the wait returned at once. A press can produce several
callbacks; to be sure nothing followed, read the window again a few seconds later
without a stop condition:

```sh
driverforge events --version current --since 2026-11-05T10:14:03Z
```

**5. Report as observed.** "One callback: `ReceivedFromProxy` on binding 5001 with
command `PULSE_VOL_UP` and the room id. The driver has no case for
`PULSE_VOL_UP`, which is why nothing happens."

**6. Adjust.** Add the case, `driverforge deploy`, and repeat the handshake with a
new marker to confirm the handler's log line appears under the event's
`correlation_id`.
