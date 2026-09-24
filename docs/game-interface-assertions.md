# Game interface assertions (v1)

Game interface assertions consume **decoded and semantically enriched**
GameTrace events. They never run in a decoder and do not extend protocol
semantic rules.

The only v1 source is a real capture session:

```yaml
api_version: gametrace.assertion/v1
kind: GameInterfaceCase
source:
  kind: capture_session
  session_id: cap_123
assertions:
  - name: login-success
    trigger: {protocol: LoginRsp, semantic: response}
    check:
      - data.code == 0
```

Omitting a lifecycle makes an assertion **Immediate**: every matching trigger
event is checked as it arrives. `eventually` passes when one matching event
passes before the timeout; `never` passes only when no event satisfies both
its trigger and checks in the timeout window.

```yaml
  - name: role-sync
    eventually: {timeout: 10s}
    trigger: {protocol: SyncRolePush}
    check: [data.roleId == $roleId]

  - name: no-error-push
    never: {timeout: 30s}
    trigger: {protocol: ErrorPush}
    check: [data.code == 100]
```

Expressions see one context document: `meta`, `data`, and `analysis`.
`meta.protocol` is the decoded event's message name; `data` is business
payload only. Supported helpers are `exists(data.path)`,
`contains(value, substring)`, and `match(value, regex)`. `$roleId` is a safe
short form for `vars.roleId`; it can be captured from a successful assertion:

```yaml
capture:
  roleId: {from: data.roleId}
```

Variables are local to one evaluation and are write-once. Event streams are
evaluated in `timestamp ASC, event_id ASC` order.
