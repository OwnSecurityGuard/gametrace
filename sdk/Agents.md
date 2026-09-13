# Agents.md

> AI/Agent development guide for `gt-plugin-sdk`.
>
> This file is the operational contract for an AI agent that needs to create or modify a GameTrace decoder plugin. It describes the **current SDK (v0.9.0) and the capture-to-plugin input contract**. It must not contain machine/environment-specific GameTrace pipeline runbooks.

## 1. Mission

`github.com/OwnSecurityGuard/gt-plugin-sdk` is the Go SDK for GameTrace decoder plugins.

A decoder plugin is an independent process that:

1. starts a gRPC `Decoder.DecodeV2` server (or joins a reverse tunnel);
2. reads and validates `plugin.yaml`;
3. registers the decoder endpoint with the GameTrace plugin registry;
4. receives capture records through `DecodeRequest`;
5. decodes zero or more logical application events;
6. returns each event as `event_type` + SDK `event.Value` MsgPack;
7. sends a final `done=true` response for every `input_id`.

The plugin contains protocol-specific decoding logic. Registration, endpoint selection, manifest loading/validation, registration retry, and heartbeat are provided by the SDK.

## 2. Non-negotiable rules for an AI agent

Before writing code:

- inspect the exact SDK version in `go.mod` (current: **v0.9.0**, requires Go 1.25.5);
- use SDK APIs instead of reimplementing registration or gRPC plumbing;
- do not import the GameTrace root project merely to obtain decoder APIs;
- do not invent protobuf messages or fields; use `sdk`, `proto`, `event`, `framing`, `rule` from this module;
- treat `DecodeRequest.payload` as a **full link-layer frame** for every pcap source; only `ProxyPayload`/`TLSPlaintext` arrive pre-stripped;
- use `link_type` as the **framing selector**, and strip via `framing.ExtractL7` rather than a hand-written offset;
- reassemble TCP streams with `framing.NewReassembler` when a message can span segments;
- do not assume one request equals one application message unless the upstream capture contract guarantees it;
- encode event payloads with `event.Value` and its MsgPack encoder, not arbitrary MsgPack objects;
- keep business data, meta and analysis in their own channels (§10.2);
- always terminate each input with `done=true`;
- never panic on untrusted network bytes;
- when the real protocol framing is unknown, inspect actual bytes before inventing a parser.

## 3. Current package layout

```text
sdk root
├── decoder.go              # DecodeFuncV2 and Decoder server wrapper
├── registry.go             # registration loop, heartbeat, endpoint logic
├── manifest.go             # ReadManifest, ResolveRegistryAddr
├── plugin_manifest.go      # Manifest types + ValidateManifest
├── tunnel.go               # RegisterOptions (Tunnel/AuthToken), reverse tunnel mux
├── contract/               # wire contract SSOT + checkers
│   ├── contract.yaml       # spec_version 6, gt.decoder/v2
│   ├── specs/schema.yaml   # SSOT for the builtin semantic vocabulary
│   ├── plugin_checker.go   # PluginChecker.Check (declaration) / CheckEvent (per-event)
│   ├── schema_index.go     # ManifestSchemaIndex: wire id -> Schema
│   └── report.go           # Violation / Report (machine-readable)
├── schema/                 # schema declaration layer (restored v0.7.0, validated since v0.7.1)
├── state/                  # state declaration layer: Subject, Change, path resolution
├── rule/                   # Protocol Semantic Rule: Predicate (GJSON) + Effect
├── framing/                # ExtractL7, Reassembler, FlowKey, Segment, TCPFlags
├── proto/
│   ├── plugin.proto        # SSOT for the gRPC contract
│   └── plugin.pb.go / plugin_grpc.pb.go
└── event/
    ├── event.go            # Event, Packet, EventContext, StateChange
    ├── identity.go / trace.go
    ├── draft.go            # Draft (plugin-side event), ToResponse, Done
    ├── value*.go           # tagged-union Value, JSON + MsgPack encoding
    └── adapter.go          # Split/MergeReservedKeys, ExtractStateChanges
```

## 4. Minimal plugin architecture

```text
plugin/
├── main.go
├── plugin.yaml
├── decoder.go
└── decoder_test.go
```

`main.go` should normally be minimal:

```go
package main

import (
    sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
    pb "github.com/OwnSecurityGuard/gt-plugin-sdk/proto"
)

func main() {
    sdk.RunRegisterLoop(decode)
}

func decode(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
    // protocol-specific decoding logic
    return nil
}
```

Do not manually create a gRPC server or manually implement registration unless a concrete requirement cannot be handled by the SDK entry points.

## 5. Registration lifecycle

`RunRegisterLoop` provides the normal lifecycle:

```text
start process
    |
    v
start DecodeV2 listener          (skipped in tunnel mode)
    |
    v
read + validate plugin.yaml
    |
    v
connect to registry
    |
    v
Register(socket_path, manifest[, tunnel])
    |
    v
receive instance_id + heartbeat interval
    |
    v
heartbeat
    |
    +---- registry connection lost ----> retry with backoff (1s .. 30s)
```

Two modes, selected by `RegisterOptions`:

```go
sdk.RunRegisterLoopWithOptions(decode, sdk.RegisterOptions{
    Tunnel:    true,          // no local listener; DecodeV2 over the Connect stream
    AuthToken: tok,           // adds `authorization: Bearer <tok>` metadata
})
```

Registration retry uses exponential backoff from 1 second up to 30 seconds. Heartbeat
default is 10s; the host marks a plugin offline after 30s. After a successful
registration, a lost heartbeat (e.g. the host restarts during an upgrade) returns the
SDK to the registration loop automatically — the plugin re-registers with a fresh
instance_id and the decoder process does not need to be restarted. Each heartbeat RPC
has a 10s timeout so a silently dead connection cannot stall reconnection.

`ResolveRegistryAddr()` resolves the registry address in this order:

1. `GT_REGISTRY_ADDR` environment variable;
2. `--registry=<addr>` command-line argument;
3. SDK fallback `:9091`.

The registry endpoint supports TCP, Unix sockets (`unix:` or a bare path) and Windows named
pipes (`npipe:`). For development and integration tests, prefer setting `GT_REGISTRY_ADDR`
explicitly.

## 6. Decoder endpoint configuration

The decoder endpoint is controlled by:

- `GT_DECODER_ADDR`
- `GT_DECODER_PUBLIC_ADDR`

If `GT_DECODER_ADDR` is unset, the SDK listens on TCP `:0` and advertises the selected address.

```text
GT_DECODER_ADDR=0.0.0.0:19001
GT_DECODER_PUBLIC_ADDR=192.168.1.10:19001
```

When the listen address is a wildcard (`0.0.0.0` / `[::]`, including the `:0` default), the
SDK **replaces the host part with the machine's first non-loopback IPv4 before registering**.
Inside Docker a wildcard host resolves to the container's own loopback, so the host could
never reach the plugin. The substitution is skipped when `GT_DECODER_PUBLIC_ADDR` is set —
that value is always used verbatim. Set it explicitly for multi-NIC / VPN setups, or to route
via the Docker bridge gateway (e.g. `host.docker.internal` on Docker Desktop).

`GT_DECODER_PUBLIC_ADDR` must carry the same port as `GT_DECODER_ADDR`, otherwise the host
dials a closed port.

## 7. Manifest contract

Every plugin needs `plugin.yaml` in its current working directory.

Minimal valid manifest:

```yaml
api_version: gt.decoder/v2
name: example-game
protocol: example-game
type: decoder
```

`ValidateManifest` (SDK side) enforces:

- `api_version` present and matching `gt.decoder/v<digit>`;
- `name` present and kebab-case, starting with a lowercase letter;
- `protocol` present;
- `type` exactly `decoder`;
- `transports`, when present, a de-duplicated subset of `tcp` / `udp`.

Version-enforcement split (important):

- SDK-side `ValidateManifest` only checks the `gt.decoder/v<digit>` pattern; it does NOT
  verify the major version against the host.
- The GameTrace host's registration-time check rejects any major other than its current
  `gt.decoder/v2`. A manifest that passes locally with `gt.decoder/v1` will be refused at
  registration — always author against `gt.decoder/v2`.

### 7.1 Optional top-level fields

```yaml
protocol_version: "1"
transports: [tcp]          # v0.8.1+: L4 transports this decoder handles
hints:
  - example-game
  - port:8080
meta:
  author: example
  description: Decoder for example game protocol
```

`hints` may carry `port:NN` entries; the platform dispatcher uses them for routing.

### 7.2 The two contract layers

A manifest may carry two independent declaration blocks. They are complementary, not
alternative: **schemas describe data shape, semantic_rules describe protocol semantics.**

```yaml
contract:
  name: gt.plugin
  version: 1
```

#### semantic_rules[] (protocol semantics)

Rules are declarations, not code: **the plugin defines protocol semantics, the platform
executes them.** Pipeline: `Event JSON → GJSON path → Predicate → Effect`.

```yaml
semantic_rules:
  - id: game.pair_request_response
    when:
      - { path: seqId, op: neq, value: 0 }
      - { path: direction, op: in, value: [client_to_server, server_to_client] }
    effect:
      type: pair
      sides:                          # key 是 per-side 的：每侧一个，可指向不同字段
        - { path: direction, op: eq, value: client_to_server, key: seqId }
        - { path: direction, op: eq, value: server_to_client, key: meta.req_seq }
  - id: game.mark_push
    when: [ { path: seqId, op: eq, value: 0 } ]
    effect: { type: annotate, semantic: notification }
  - id: game.extract_sync_db
    when: [ { path: data.SyncDbData, op: exists } ]
    effect:
      type: extract
      source: data.SyncDbData
      child: { event_type: game.db.update, schema_id: game.db.update.v1 }
  - id: game.name_message
    when: [ { path: type, op: exists } ]
    effect: { type: name, key: type }        # v0.8.2+
```

Effect set is closed: `pair`, `extract`, `annotate`, `name`.

- `pair` — exactly 2 `sides`; each side has its own `key` (GJSON path of the pairing
  value on that side — the two sides may point at different fields) plus role
  discrimination. There is no rule-level `key`.
- `extract` — `source` (array → one child per element; object → one child; scalar/null →
  none) plus `child.event_type` + `child.schema_id`.
- `annotate` — `semantic` ∈ request / response / notification / error. Error and notification
  are Event attributes, not Relations.
- `name` (v0.8.2) — `key` is the GJSON path of the message name. The host writes the first
  hit into `meta.msg_name`. `name` rules are evaluated **before** all others and the extracted
  value is injected into the evaluation view as `_meta.msg_name`, so later `when` predicates
  may branch on it. Rule-declared names win over decoder-hardcoded ones.

Predicate ops are a closed set: `eq`, `neq`, `exists`, `not_exists`, `gt`, `gte`, `lt`,
`lte`, `in`, `not_in`, `contains`, `prefix`, `suffix`. `in` / `not_in` require an array
`value`. `when` may be a sequence (implicit `all`) or an explicit `all` / `any` combinator;
combinators must not mix with each other nor carry leaf conditions.

Rule IDs are dot-separated lowercase segments, unique, without a version suffix.

No Expr, no scripts, no custom functions. Group and advanced causal relations (caused-by
etc.) are intentionally deferred until real protocol facts demand them.
Full spec: `docs/plugin-semantic-rules.md`.

## 8. `DecodeRequest`: the most important semantic rule

The current protobuf request contains:

```text
session_id      string
protocol_hint   string
payload         bytes
link_type       int32
input_id        string
packet_id       string
flow_id         string
src             string
dst             string
direction       string
timestamp_ns    int64
```

### 8.1 `payload` framing is decided by `link_type`

**This is the single most important contract, and the easiest one to get wrong.**

For every pcap-based source (`pcap-live`, `pcap-file`), the capture layer parses L2/L3/L4
*only to populate the context fields*. It never trims `payload`:

```text
Capture
  |
  +-- parse L2 --> link_type          (context only)
  +-- parse L3 --> src / dst          (context only)
  +-- parse L4 --> protocol_hint      (context only)
  |
  +-- packet.Data() --> Raw --> DecodeRequest.payload   (UNTOUCHED, full frame)
```

Only the two proxy-class link types deliver pure L7:

| link_type | source | payload contains |
|---|---|---|
| `0` Null / `108` Loop | loopback capture | 4B `AF_*` header + IP + transport + **L7** |
| `1` Ethernet | NIC capture | 14B Ethernet + IP + transport + **L7** |
| `113` LinuxSLL | Linux cooked | 16B SLL + IP + transport + **L7** |
| `101` Raw / `1000` RawIP | tun / VPN | IP + transport + **L7** |
| `228` / `229` | IPv4 / IPv6 | IP + transport + **L7** |
| `1001` ProxyPayload | proxy | **L7 only** |
| `1002` TLSPlaintext | decrypted TLS | **L7 only** |

A decoder **must** strip according to `link_type`. Do not hand-roll it:

```go
seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
if !ok || len(seg.Payload) == 0 {
    return stream.Send(event.Done(req.GetInputId()))
}
```

> **History.** Earlier revisions of this document and of `contract.yaml` asserted
> "`payload` is L7, never strip headers", enforced at `error` severity via the rule
> `payload-is-l7`. That was factually wrong for every pcap path — the reference `http`
> plugin itself has always stripped headers. A decoder written to that rule produces **zero
> events** on real traffic. The rule is now `payload-framing-by-link-type`.

### 8.2 What `link_type` means

`link_type` is the **framing selector** — how many bytes of encapsulation sit in front of the
application data. It is *not* merely provenance: the same decoder will receive loopback
frames (`0` / `108`, 4-byte header) and Ethernet frames (`1`, 14-byte header) in the same
session, so a hardcoded offset that works on one silently corrupts the other.

Numeric values are fixed by the SDK in `event/event.go` and mirrored in `contract.yaml`
under `link_types`. `framing.ExtractL7` already encapsulates the full mapping — prefer it
over a hand-written switch.

### 8.3 Other request fields

- `input_id`: identity of this Decode RPC input. Every response for this input must use the same value.
- `session_id`: capture/analysis session.
- `packet_id`: originating captured packet ID when available.
- `flow_id`: network flow context when available.
- `direction`: ingress/egress direction when available.
- `protocol_hint`: pipeline-provided protocol hint.
- `src` / `dst`: parsed source/destination address context.
- `timestamp_ns`: capture timestamp in nanoseconds.

Do not silently reinterpret missing context as a known business value.

## 9. How to validate framing before decoding

When implementing a decoder for an unknown protocol, do **not** immediately reverse-engineer
the business message. First verify what bytes actually reach the plugin. **Use the host's
sampling tool rather than writing your own byte-dumping code** — it is a single call and
needs no rebuild:

```text
sample_bytes_plugin(session_id=..., limit=20, max_bytes=64)
```

It returns a hexdump plus a length histogram and first-byte distribution. Read the first
bytes against this table:

| first bytes | meaning |
|---|---|
| `02 00 00 00` | loopback `AF_INET` header — full frame, strip 4B then IP/TCP |
| `45 ..` | bare IPv4 header — strip IP/TCP |
| `ff ff ff ff ff ff` / any 14B then `08 00` | Ethernet frame — strip 14B then IP/TCP |
| `47 45 54 20` (`GET `), `48 54 54 50` (`HTTP`) | already L7 |

Then answer, in order:

1. What is the framing — full frame or already L7? (`link_type` + first bytes)
2. Is the data plaintext, TLS, WebSocket, compressed, or binary?
3. Is one request one application message, or only a stream fragment?
4. Is there a protocol magic/header/opcode?
5. Does `protocol_hint` agree with the observed bytes?

Only after those questions are answered should the plugin implement message decoding.

**Do add link-layer stripping to the decoder — the capture layer has *not* done it for you.**

## 10. `DecodeResponseV2`: output contract

The current response contains:

```text
input_id             string
done                 bool
event_type           string
payload_msgpack      bytes
meta_msgpack         bytes   (v0.8.0+)
analysis_msgpack     bytes   (v0.8.0+)
error                string
correlation_key      string
causation_input_id   string
```

### 10.1 Normal sequences

```text
response(input_id=X, done=false, event_type=..., payload_msgpack=...)
response(input_id=X, done=false, event_type=..., payload_msgpack=...)
...
response(input_id=X, done=true)
```

No decoded event:

```text
response(input_id=X, done=true)
```

Decode error:

```text
response(input_id=X, error="...", done=true)
```

The SDK wrapper also converts callback errors into an error response and catches callback
panics, returning a `decoder panic: ...` error with `done=true`.

### Critical rule: `done=true`

`done=true` is the completion marker for an `input_id`. Every input must eventually receive
it. Do not forget it. Do not mark an ordinary intermediate event as `done=true`.

### 10.2 Payload / Meta / Analysis split (v0.8.0+)

Business data, network meta facts and platform analysis are three separate channels:

| channel | field | carries |
|---|---|---|
| Payload | `payload_msgpack` | business fields only |
| Meta | `meta_msgpack` | network/meta facts: `direction`, `msg_name`, `role`, `is_push`, `flow_id` |
| Analysis | `analysis_msgpack` | `_state_changes`, `entity`, `entity_type`, `entity_id`, `change_count` |

`event.Draft` expresses this directly:

```go
draft := event.Draft{
    Type:           "game.move",
    Value:          businessValue,        // root must be object
    Meta:           metaValue,            // optional, omitted when null
    Analysis:       analysisValue,        // optional, omitted when null
    CorrelationKey: flowKey.Canonical(),
}
resp, err := draft.ToResponse(req.GetInputId())
```

Older plugins put `_meta` / `_state_changes` / `direction` at the top level of the payload.
That still works: `event.SplitReservedKeys` splits a flat value into (business, meta,
analysis) and `MergeReservedKeys` is its inverse. **New plugins should use `Draft.Meta` /
`Draft.Analysis`** — do not mix business data into reserved keys.

If the decoder observes mutable entity state (player HP, item count, login status, ...), it
MUST declare it in the analysis channel as `_state_changes`. Each item needs at least
`subject_type`, `subject_id`, `op` (`set` / `delete` / `merge`) and `path`. Omitting it is
not an error, but those events then cannot participate in state projection.

## 11. `event.Value` API cheat sheet

`event.Value` is a tagged union. Its kinds are:

```text
Null, Bool, Int, Uint, Float, String, Bytes, Array, Object
```

### Constructors

```go
event.ValueNull() / ValueBool(true) / ValueInt(42) / ValueUint(42)
event.ValueFloat(3.14) / ValueString("hello") / ValueBytes(data)
event.ValueArray(items) / ValueObject(fields)
```

Conversion helpers: `ValueFromAny`, `ValueFromMap`, `ValueFromSlice`, `ValueFromJSON`,
`ValueFromJSONMap`.

### Accessors always use `(value, ok)`

```go
s, ok := v.AsString()
i, ok := v.AsInt()
f, ok := v.AsFloat()
o, ok := v.AsObject()
```

Path/object helpers: `v.Get("field")`, `v.GetByPath("data.player_id")`, `v.Index(0)`,
`v.Len()`. Mutation helpers return a new `Value` and an error: `Set`, `Delete`, `Append`,
`Merge`.

### Common AI mistakes

```go
s := v.AsString()        // WRONG — ignores ok
s, ok := v.AsString()    // correct

name := v.String         // WRONG — String is a method on ValueKind
name, ok := v.AsString() // correct
```

Also avoid treating `Value` as a normal `map[string]any`. Use `Get`, `GetByPath`, `AsObject`,
or `ToAny()` when conversion to native Go values is actually required.

### Float folding trap

`ValueFromMap` / `ValueFromAny` fold integral `float64` values into `Int`: `float64(0)`,
`float64(1)` become `Int`. A field declared `float64` in `schemas` then mismatches its wire
kind and the host reports a schema violation — invisible to the compiler and to unit tests.
Coordinates and orientations hit this constantly (they are often integral). Work around it
with a float marker type and a custom converter instead of `event.ValueFromMap`.

## 12. Event payload encoding

The event wire payload must use the SDK's tagged MsgPack representation.

```go
value := event.ValueFromMap(map[string]any{
    "player_id": "p001",
    "level":     42,
    "online":    true,
})
payload, err := value.MarshalMsgpack()
```

For JSON protocol data use `event.ValueFromJSON(data)` then `MarshalMsgpack`.

Do **not** replace this with `msgpack.Marshal(map[string]any{...})`.
`event.UnmarshalValueMsgpack` is the corresponding decoder.

## 13. Event model and relationships

Conceptually:

```text
Event
├── Identity  (ID, SessionID, Type, SchemaID, Source, Timestamp)
├── Trace     (CausationID, CorrelationID, OriginID)
├── Context   (FlowID, RawPacketID, MessageOrdinal, Direction)
└── Payload   (SchemaID, Value)
```

- **CausationID**: direct event that caused this event;
- **CorrelationID**: business/process chain grouping related events;
- **OriginID**: original source event for the derived chain.

At the DecodeV2 level, `correlation_key` is a stable business/protocol correlation key when
one is actually known, and `causation_input_id` identifies the Decode input that directly
caused the emitted event when that relation is actually known. Do not fabricate IDs merely
to fill fields, and do not confuse:

```text
input_id  != packet_id != flow_id != event ID
```

## 14. Event type

`event_type` describes **what happened** (`http.request`, `game.login`, `combat.attack`).
Prefer a stable, meaningful event type; keep the payload shape backward compatible, or
introduce a new event type when the shape breaks compatibility.

## 15. Protocol decoding strategy

```text
1. inspect req.payload
2. confirm L7 framing        (framing.ExtractL7)
3. validate enough bytes for the protocol header
4. identify message/frame boundaries
5. decode one or more logical messages
6. convert decoded data to event.Value
7. MarshalMsgpack
8. Send done=false event response(s)
9. Send done=true
```

For stream-oriented protocols, do not assume a request is a complete application message
unless the capture/pipeline contract guarantees message framing.

For malformed input: return an error when the whole input cannot be decoded; skip an
individual malformed sub-message only when framing permits safe recovery; never panic on
untrusted bytes.

## 16. Recommended implementation pattern

```go
func decode(req *pb.DecodeRequest, stream pb.Decoder_DecodeV2Server) error {
    inputID := req.GetInputId()
    defer func() {
        if r := recover(); r != nil {
            _ = stream.Send(&pb.DecodeResponseV2{
                InputId: inputID, Done: true,
                Error: fmt.Sprintf("decoder panic: %v", r),
            })
        }
    }()

    seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
    if !ok {
        return stream.Send(event.Done(inputID))
    }

    st := d.reasm.Push(seg) // push even empty payloads: flags drive per-flow state
    for {
        buf := st.Bytes()
        if len(buf) == 0 || len(buf) > maxStreamBytes {
            break
        }
        msg, n, ok := parseMessage(buf)
        if !ok {
            break // incomplete: wait for more segments
        }
        if msg != nil {
            if err := d.emit(stream, inputID, seg.Flow.Canonical(), msg); err != nil {
                return err
            }
        }
        st.Consume(n)
    }
    return stream.Send(event.Done(inputID))
}
```

Keep protocol parsing separate from RPC transport where practical:

```text
DecodeV2 callback -> protocol parser -> normalized message -> event.Value -> DecodeResponseV2
```

This makes parser tests independent from gRPC. Cap per-flow reassembly memory (the reference
plugin uses 4 MiB) so an unparseable flow cannot grow without bound.

## 17. Testing requirements

### Protocol correctness

valid message; multiple messages in one input; empty payload; truncated header; truncated
body; unknown opcode/message type; malformed encoding; boundary lengths; representative real
capture bytes.

### Event output

Verify exact `event_type`; `payload_msgpack` decodes with
`event.UnmarshalValueMsgpack`; expected Value kinds and fields; every decode input eventually
emits `done=true`.

### Framing regression

Keep at least one fixture per `link_type` the decoder will actually see, captured from
**real traffic** via `sample_bytes_plugin` — at minimum a loopback frame (`0` or `108`) and
an Ethernet frame (`1`). Do **not** build fixtures by hand-writing an L7 snippet: a synthetic
L7 fixture makes the stripping path untestable, and the decoder will pass its own tests while
producing zero events against real capture.

### Fake stream

`pb.Decoder_DecodeV2Server` is a generic interface in grpc 1.71. A fake must implement
`Send`, `Recv`, `Context`, `SetHeader` / `SendHeader` / `SetTrailer`, `SendMsg` / `RecvMsg`.

### Declaring conformance locally

Feed decoded events straight into the checker to prove "the decoded output matches the
manifest declaration" in one shot:

```go
m, _ := sdk.ParseManifest(data)                  // data = plugin.yaml source
checker := contract.NewPluginChecker()
checker.Check(m)                                 // declaration: schema + semantic layers
checker.CheckEvent(m, draft)                     // runtime: rules evaluate
```

## 18. Development workflow

```text
1.  read this Agents.md (esp. §8.1 framing) and get_plugin_contract
2.  inspect SDK version in go.mod
3.  capture a short real session, then sample_bytes_plugin to see the FIRST BYTES
4.  decide framing from link_type + observed bytes -- before writing any parser
5.  create minimal plugin.yaml
6.  create minimal RunRegisterLoop entry point
7.  strip with framing.ExtractL7; add framing.Reassembler if messages span segments
8.  implement a parser over the resulting L7 []byte
9.  save the sampled real frame as a fixture (one per link_type)
10. convert decoded data through event.Value (+ Meta / Analysis channels)
11. send event responses
12. send done=true
13. add malformed-input tests
14. run gofmt / go test ./... / go vet ./...
15. validate with contract.NewPluginChecker (Check + CheckEvent)
```

A complete, runnable reference for steps 5–12 lives at `examples/http-stream-decoder`.

Do not start by implementing the entire protocol. First prove one real message end-to-end.

## 19. What belongs outside this SDK document

Do not put environment-specific GameTrace pipeline observations here, including:

- which local ports are used by a particular GameTrace deployment;
- whether a particular Windows interface captures loopback traffic;
- which process owns a port in a particular deployment;
- MCP control-port noise;
- SQLite lock behavior in a particular pipeline version;
- Windows process-killing commands;
- version-specific pipeline bugs.

Those are deployment/runbook concerns and belong in the GameTrace main repository's Agent
documentation or pipeline debugging documentation.

## 20. When an AI agent must ask for clarification

Ask the user instead of guessing when any of the following is unknown and materially affects
decoding:

- the real application protocol or message framing;
- whether a payload fixture is request or response direction;
- the meaning of a game-specific opcode/field;
- the required event types/schema IDs when no project convention exists;
- a protocol encryption/compression layer that cannot be resolved from available evidence;
- a required correlation rule that cannot be inferred safely.

Do **not** ask for clarification merely because an implementation detail can be verified from
this SDK. Inspect the SDK first.

## 21. Quick checklist before declaring a plugin complete

```text
[ ] plugin.yaml validates (ValidateManifest + PluginChecker.Check)
[ ] RunRegisterLoop / RunRegisterLoopWithOptions is used
[ ] registry address is configurable
[ ] decoder endpoint is configurable
[ ] DecodeRequest.payload framing matches link_type (framing.ExtractL7 used)
[ ] TCP reassembly is in place if messages can span segments
[ ] real captured bytes were inspected via sample_bytes_plugin
[ ] a real-frame fixture exists per link_type (loopback + ethernet)
[ ] protocol framing is explicit
[ ] event.Value is used; accessors handle the (value, ok) return form
[ ] tagged MsgPack is used
[ ] business / meta / analysis stay in their own channels
[ ] mutable entity state is declared via _state_changes
[ ] event_type is stable and meaningful
[ ] correlation/causation are only set when known
[ ] malformed input cannot crash the process
[ ] done=true is emitted for every input
[ ] parser tests exist
[ ] gofmt / go test ./... / go vet ./... pass
[ ] semantic_rules pass the host registration-time checker (gt.semantic.*)
[ ] event_type does not use the reserved gt. prefix
```

## 22. Additional SDK public APIs

### 22.1 `framing.ExtractL7`

```go
seg, ok := framing.ExtractL7(raw []byte, linkType int32) // (Segment, bool)
```

```go
type Segment struct {
    Payload  []byte    // application-layer bytes (aliases raw; copy if retained)
    Flow     FlowKey   // directional stream this chunk belongs to
    Seq      uint32    // TCP sequence of Payload[0]; 0 for UDP / proxy
    Flags    TCPFlags  // FIN/SYN/RST/PSH/ACK/URG; zero for UDP / proxy
    IsTCP    bool
    LinkType int32
}
```

### 22.2 `framing.Reassembler`

```go
ra := framing.NewReassembler()
st := ra.Push(seg)            // Stream for this FlowKey
buf := st.Bytes(); st.Consume(n)
ra.Forget(key)                // drop one flow without waiting for FIN
ra.Reset()                    // clear ALL state between capture runs
```

TCP reassembly is inherently cross-packet: the reassembler must live on the decoder struct,
not inside `decode`. Never share one `Reassembler` across unrelated captures without a
`Reset()` between — stale sequence state silently corrupts the next run (and makes a second
replay of the same capture produce zero events).

### 22.3 `framing.FlowKey`

```go
type FlowKey struct { Src, Dst netip.AddrPort; Protocol string }
f.Reverse()      // opposite direction
f.Canonical()    // direction-independent id: pairs a request with its response
f.String()       // "proto src>dst"
```

Use `Canonical()` as the correlation key between request and response events; use `FlowKey`
itself for per-direction state.

### 22.4 Reading meta back

`event.Event.MetaValue("direction")` is the supported accessor for reserved meta values
(`direction`, `flow_id`, `msg_name`, `is_push`). Do not reach into `event.Payload` internals.

### 22.5 Reading `_state_changes` back

`event.ExtractStateChanges(value)` reads the reserved `_state_changes` key from a Value
or the Analysis channel and returns the projected `[]event.StateChange`.
