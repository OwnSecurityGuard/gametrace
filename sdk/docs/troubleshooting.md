# GameTrace Decoder Plugin Troubleshooting Runbook

This document contains runtime and development observations that are useful when developing a plugin against the GameTrace environment. They are **operational observations, not SDK API guarantees**. If GameTrace Pipeline behavior changes, update this document.

The goal is to make an AI agent able to diagnose a plugin without access to the GameTrace main repository.

## 1. Debugging priority

When a plugin produces no useful events, do not immediately change the protocol parser.

Use this order:

```text
1. Is the plugin running?
2. Did it register with the intended registry?
3. Is the decoder endpoint reachable?
4. Is the plugin receiving requests?
5. Is payload a full frame that still needs ExtractL7 stripping (pcap), or already L7 (ProxyPayload/TLSPlaintext)?
6. Is the payload encrypted/opaque?
7. Is the capture flow the intended one?
8. Is the parser framing correct?
9. Is event.Value / MsgPack output correct?
10. Only then change business decoding logic.
```

## 2. Windows process cleanup

When iterating on a plugin under Windows, avoid assuming that `taskkill` from Git Bash will behave like native PowerShell/cmd execution. In some environments it can fail silently or produce misleading output.

Prefer an explicit PowerShell process cleanup when the PID is known:

```powershell
Stop-Process -Id <PID> -Force
```

Then verify the process is gone before restarting it.

For a named executable:

```powershell
Get-Process <name> -ErrorAction SilentlyContinue
```

Do not repeatedly restart processes without first confirming which instance owns the decoder endpoint.

## 3. Registry address

During development, set the registry address explicitly rather than relying on the SDK fallback:

```text
GT_REGISTRY_ADDR=<actual registry endpoint>
```

This prevents debugging the wrong registry instance.

After restart, verify registration before investigating decode results.

The expected sequence is:

```text
start plugin
  -> plugin registers (Register RPC returns instance_id)
  -> plugin opens the tunnel Connect stream, instance_id in stream metadata
  -> registry reports plugin instance
  -> only then start capture/decode
```

## 4. Port selection observations

The following are environment observations from the GameTrace setup as of August 2026. They are not SDK guarantees:

- `8099` is used by gametrace-mcp's control endpoint in the observed setup. Traffic there is MCP/control noise and should not be treated as game traffic.
- `8087` was observed to be TLS-only. An application decoder cannot recover plaintext business fields from it without the required decryption context.
- Several services bound to `127.0.0.1` were not observable through the selected network capture path. In particular, attempts around ports `8088`, `8064`, and `8062` demonstrated that loopback visibility depends on the capture mechanism/interface.

Before selecting a target port, verify ownership and listener address with native Windows tooling, for example:

```powershell
Get-NetTCPConnection -LocalPort <PORT> -ErrorAction SilentlyContinue
```

Do not infer "no traffic" solely from the absence of packets on an ordinary Ethernet interface.

## 5. Loopback capture

Loopback traffic is special. A process bound to `127.0.0.1` can communicate successfully while remaining invisible to the capture interface you are watching.

If the application works but the plugin sees nothing:

```text
application works
       |
       +--> check listener address
       |
       +--> check selected capture interface
       |
       +--> check loopback support
       |
       +--> check whether the capture backend exposes loopback frames
```

Do not change the decoder parser until capture visibility is established.

An observed Npcap loopback representation used an Ethernet type value of `0x0000`. Treat this as an environment-specific capture representation, not a universal protocol rule.

## 6. Wrong traffic / MCP noise

A decoder can be perfectly correct and still emit only unknown events if it receives traffic from the wrong service.

Check:

- source/destination address;
- source/destination port;
- protocol;
- flow ID;
- direction;
- timestamp;
- whether the selected port belongs to GameTrace/MCP itself rather than the game.

If all events look structurally unrelated to the target game protocol, inspect the flow metadata before rewriting the parser.

## 7. All events are `unknown`

Use this checklist in order:

### A. Inspect payload bytes

Capture the first 16-32 bytes from the actual `DecodeRequest.payload`.

If the bytes do not resemble the protocol you expected, stop protocol analysis.

### B. Confirm L7 boundary (the single most common zero-event cause)

For **every pcap source** (`pcap-live`, `pcap-file`) `DecodeRequest.payload` is a
**complete link-layer frame**: link header + IP + transport + application bytes. The
capture layer never strips it for you — you **must** strip according to `link_type`:

```go
seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
if !ok || len(seg.Payload) == 0 {
    return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
}
```

Only `ProxyPayload` (`1001`) and `TLSPlaintext` (`1002`) arrive pre-stripped (pure
L7); `ExtractL7` passes those through unchanged, so always call it.

> A decoder that assumes `payload` is already L7 (no stripping) produces **zero
> events** on real traffic. This is the most frequent "all events unknown / zero
> events" root cause. The full framing contract is in `Agents.md` §8.1 — treat it as
> authoritative; this runbook only records the symptom.

### C. Check TLS/encryption

Look for TLS-like records or otherwise opaque encrypted traffic. If encrypted, the decoder may only be able to classify the transport/protocol state.

### D. Check capture target

Verify that the captured connection is actually the game connection rather than GameTrace control traffic.

### E. Check loopback

If the application uses `127.0.0.1`, verify the capture backend/interface exposes that traffic.

### F. Replay a known fixture

Feed a known payload directly to the decoder. If replay succeeds but live capture fails, the parser is probably not the primary problem.

## 8. SQLite busy / capture startup ordering

In the observed GameTrace development environment, starting/stopping capture while another process still owns the SQLite database can result in `SQLITE_BUSY` or related lock errors.

Use this ordering:

```text
stop old plugin/processes if necessary
        |
        v
verify stale processes are gone
        |
        v
start registry/plugin
        |
        v
verify registration
        |
        v
start capture
        |
        v
query with bounded result size
```

Do not interpret a database lock error as a decoder protocol failure.

## 9. Query discipline: never fetch unbounded decoded data

During agent-driven debugging, queries such as `list_decoded_data` can return very large result bodies.

Default to a small bounded limit, preferably:

```text
limit <= 50
```

Only increase it when the specific diagnostic requires more data.

Use pagination or targeted filters rather than repeatedly requesting the full decoded dataset.

This is both faster and substantially cheaper in agent context/token usage.

## 10. Build/test loop

Use a tight loop:

```text
edit
  |
  v
go test ./...
  |
  v
fix compile/test errors
  |
  v
build plugin
  |
  v
stop old process
  |
  v
start with explicit GT_REGISTRY_ADDR
  |
  v
verify registration
  |
  v
start capture
  |
  v
send/query a small bounded sample
```

Avoid adding speculative imports or helper packages before the compiler tells you they are required.

When the SDK API is unfamiliar, read the local SDK source or `Agents.md` instead of doing repeated blind compile cycles.

## 11. Re-registration does not retroactively decode old events

A common debugging trap is:

```text
capture traffic
  -> plugin has bug
  -> fix plugin
  -> restart/re-register plugin
  -> query old decoded events
```

Re-registering the corrected plugin does not imply that already stored events are automatically decoded again.

After a decoder fix, use a fresh capture or a deterministic replay fixture.

This makes the test causal:

```text
known input
  -> corrected decoder
  -> expected event
```

rather than mixing old and new decoder behavior in one dataset.

## 12. Registration / tunnel troubleshooting

The platform runs plugins in tunnel mode only (`GT_TUNNEL=1`, injected by gt-agent and by
Developer Plane `activate_plugin`): the plugin opens no listener and the host never dials back.
"Registered but never online / no decoding" therefore means the `Connect` stream failed —
almost always a plugin built with an older SDK that does not send `instance_id` in the stream
metadata (the host rejects it), or a dropped tunnel/heartbeat. Rebuild with the current SDK
and check the plugin log for a rejected stream.

Registration success proves the registry can communicate with the plugin; it does not prove that the complete decode request path is healthy.

## 13. When to ask for help

Stop debugging by trial-and-error and ask for concrete information when:

- the capture interface behavior is unclear;
- the target service/port is uncertain;
- the payload is encrypted and no key/decryption context exists;
- the expected protocol framing is undocumented;
- an SDK API appears different from `Agents.md`;
- a pipeline behavior contradicts this runbook.

Ask for the smallest useful artifact: one payload hex dump, one `DecodeRequest` fixture, one registration log, or one endpoint/process mapping.

Do not request an entire unbounded capture unless the smaller artifact is insufficient.

## 14. Staleness rule

Everything in this file outside the SDK contract is an observation about the GameTrace runtime environment. If a port, capture backend, process topology, or command behavior changes, update the relevant section and record the observation date/version.

The SDK API and protocol contract belong in `Agents.md`; this file should not redefine them.
