# Example: verify a change against the previous version

**Situation.** The driver reconnects to the device every time the network binding
flaps, and the developer wants it to back off instead. The reconnect runs on a
timer the driver starts itself, so no person is needed.

**1. Baseline.** Before editing, note the running version:

```sh
driverforge logs --version current --tail 1 | jq -r '.scope.version'   # 2026110507
```

**2. Change and deploy.** Add the back-off, `driverforge deploy`, note
`"version": "2026110508"`.

**3. Trigger.** The deploy reloads the driver, which connects and starts the
reconnect timer.

**4. Read both versions the same way.**

```sh
driverforge logs --version 2026110507 --grep 'reconnect' --tail 50 > prev.json
driverforge logs --version current --grep 'reconnect' --since 5m \
  --min-events 3 --timeout 2m > new.json
jq '[.entries[] | .timestamp]' prev.json new.json
jq '.count' prev.json new.json
driverforge errors --version current | jq '.count'
```

**5. Compare.** The previous version logged a reconnect every second; the new one
logs at 1s, 2s, 4s, 8s. `errors` returns `count: 0` for the new version, as it
did for the old.

**6. Report.** "The back-off works: reconnect attempts on 2026110508 double from 1s
to 8s, where 2026110507 retried every second. No errors on the new version." Then
remove the temporary `reconnect` logging if it was added for this check.

If `new.json` came back with `wait.satisfied: false` (exit 3), the new version
never reconnected in two minutes: check `scope.version` and the deploy's `state`
before concluding anything about the back-off.
