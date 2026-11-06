# Example: an undocumented wrapper

**Situation.** The driver polls a vendor's status endpoint on startup. The vendor's
docs say the response is `{"power": "on", "input": "hdmi1"}`, the parser reads
`response.power`, and the driver's Power property never updates.

**1. Instrument.** The response arrives in the `C4:urlGet` callback, which isn't
recorded on its own. Log the raw body before parsing:

```lua
C4:urlGet(url, headers, false, function(ticket, data, code, headers, err)
    Log:Debug("vendor-raw status", code, data)
    ParseStatus(data)
end)
```

**2. Deploy.** `driverforge deploy` returns `"state": "confirmed"`,
`"version": "2026110502"`.

**3. Trigger.** The poll runs from OnDriverLateInit, so the deploy was the trigger.

**4. Read.**

```sh
driverforge logs --version 2026110502 --until-match 'vendor-raw' --timeout 30s \
  | jq '{scope: .scope.version, wait: .wait.satisfied, msg: [.entries[].message]}'
```

```json
{"scope": "2026110502", "wait": true,
 "msg": ["vendor-raw status 200 {\"data\":{\"power\":\"on\",\"input\":\"hdmi1\"},\"ts\":1762333200}"]}
```

`scope` is the new version and the wait was satisfied, so this is the right
evidence.

**5. Compare.** The fields sit under an outer `data` object the docs don't
mention. The parser reads `response.power`, which is nil.

**6. Adjust and repeat.** Parse `response.data`, keeping the top level as a
fallback, lower the raw log to `Debug` or remove it, then `driverforge deploy`
(version `2026110503`) and confirm:

```sh
driverforge logs --version 2026110503 --until-match 'power=on' --timeout 30s
driverforge errors --version 2026110503
```

The expected log appears and `errors` returns `count: 0`. Done. The developer
didn't paste anything.
