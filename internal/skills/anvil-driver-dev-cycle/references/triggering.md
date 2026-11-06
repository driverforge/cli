# Triggering

The loop only works if you know what causes the activity you want to observe.

## Find the project's trigger

1. Look in the driver repo: the README, any notes for agents (`AGENTS.md`,
   `CLAUDE.md`), and comments near the handler.
2. Check whether startup can answer the question. Deploying reloads the driver,
   so anything OnDriverInit or OnDriverLateInit does — fetching defaults from a
   vendor API, opening a connection, starting a polling timer — happens without
   anyone's help.
3. If neither answers it, ask the developer once what causes the action, and
   suggest they write it down in the repo so the next session doesn't ask.

## Triggers

| Trigger | Examples | What you do |
| --- | --- | --- |
| Driver-initiated | Reload on deploy, timers, polling | `driverforge deploy` and read. No person needed |
| Controller or UI action | A button in the Control4 app, a programming event, a room or device command | The handshake below |
| Vendor-initiated | The device or its cloud pushes an update | Cause it from the vendor side if you can (its app, its API); otherwise ask the developer |

There is no scripted way to fire a UI action today. When the question allows it,
move the work under test onto a driver-initiated path (for example a temporary
call from OnDriverLateInit) so you can trigger it yourself, and remove it after.

## Handshake for a person-triggered action

1. Record a marker before asking:

   ```sh
   date -u +%Y-%m-%dT%H:%M:%SZ
   ```

2. Tell the developer the exact action — which room, which device, which button —
   and that you are waiting for it. Never ask them to paste logs afterwards; you
   read them yourself.
3. Run one bounded wait scoped by the marker:

   ```sh
   # when you know the entry to expect
   driverforge logs --version <n> --since <marker> --until-match '<regex>' --timeout 60s
   # when exploring
   driverforge events --version <n> --since <marker> --min-events 1 --timeout 60s
   ```

   `--follow --timeout 60s` streams everything in the window when you can't name a
   stop condition.
4. Read the entries oldest first and report the sequence as observed.

If the wait exits 3, ask whether the action was done before waiting again. Don't
widen the window or change the code first.
