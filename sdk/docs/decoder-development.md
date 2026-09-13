# Decoder Plugin Development Guide

This guide is the practical companion to `Agents.md`. It is written so an AI agent can build and debug a GameTrace decoder plugin using **only this repository**.

## 1. Start here

Before writing code:

1. Read `Agents.md` completely.
2. Inspect the exact SDK version in `go.mod`.
3. Create the smallest plugin possible: `main.go`, `plugin.yaml`, decoder code, tests.
4. Get one real payload fixture before implementing a large parser.
5. Verify registration and one successful decode before adding protocol features.

Do not begin by reverse-engineering the entire protocol from guesses.

## 2. The actual data boundary

The GameTrace capture layer parses L2/L3/L4 **to fill in context fields only**. It does not
trim the payload. For every pcap source the plugin receives the **complete link-layer
frame**.

Conceptually:

```text
Captured frame
    |
    +-- L2 parsed ------------------> link_type          (context only)
    +-- L3 parsed ------------------> src / dst          (context only)
    +-- L4 parsed ------------------> protocol / TCPFlags(context only)
    +-- capture metadata -----------> request/session context
    |
    +-- packet.Data() (whole frame) -> DecodeRequest.payload
```

Therefore:

> For pcap sources, `DecodeRequest.payload` **is** a full frame: link-layer header +
> IP + TCP/UDP + application bytes. Only `ProxyPayload` (1001) and `TLSPlaintext`
> (1002) deliver bare L7.

You **must** strip according to `link_type`. Use the SDK helper rather than
hand-written offsets — it covers every link type, including both loopback encodings:

```go
seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
if !ok || len(seg.Payload) == 0 {
    return stream.Send(&pb.DecodeResponseV2{InputId: req.GetInputId(), Done: true})
}
// seg.Payload is L7; seg.Flow identifies the stream; seg.Seq/seg.Flags carry TCP state.
```

`link_type` is the framing selector, not mere provenance. See
[link-type-reference.md](./link-type-reference.md) for the byte layout of each type.

## 3. First real-payload inspection

When a protocol is unknown or events are all `*.unknown` — or, most commonly, when you
get **zero events** — inspect the actual bytes before touching the parser.

Do this with the host's sampling tool, not with your own debug code. It requires no
rebuild and no redeploy:

```text
sample_bytes_plugin(session_id=..., limit=20, max_bytes=64)
```

Match the first bytes against this table before assuming anything:

| first bytes | framing |
|---|---|
| `02 00 00 00` | loopback, `AF_INET` — 4B header, then IP/TCP |
| `1e 00 00 00` | loopback, `AF_INET6` — 4B header, then IP/TCP |
| `45 ..` | bare IPv4 — no link header |
| 14 bytes then `08 00` | Ethernet + IPv4 |
| `47 45 54 20`, `50 4f 53 54`, `48 54 54 50` | already L7 (HTTP) |

The purpose is to answer, in order:

- What is the framing — full frame or bare L7?
- Is it plaintext, TLS, WebSocket, compressed, encrypted, or a custom binary frame?
- Does it contain a recognizable protocol magic/header?
- Is one packet one application message, or only a stream fragment?

Do not spend multiple rounds decoding individual fields until the framing assumption is
verified against real bytes. A wrong framing assumption is the single most expensive
mistake in decoder development: it produces zero events with no error message, which
looks identical to "no traffic".

## 4. Detect the application protocol before decoding

A useful development order is:

```text
raw L7 bytes
   |
   +-- TLS-looking? ----------> encrypted/opaque handling
   |
   +-- HTTP-looking? ---------> HTTP parser
   |
   +-- WebSocket-looking? ----> WS framing
   |
   +-- known binary magic? ---> game protocol parser
   |
   +-- otherwise -------------> unknown/forensics path
```

The exact protocol detection rules belong to the plugin, not the SDK.

Never assume that a `protocol_hint` means the payload has already been decoded. Treat it as a hint unless the plugin's integration contract explicitly guarantees stronger semantics.

## 5. Separate framing from business decoding

Prefer four layers. The first two are supplied by the SDK — do not reimplement them:

```text
DecodeRequest.payload  (full frame)
       |
       v
framing.ExtractL7      <-- SDK: strips link/network/transport per link_type
       |
       v
framing.Reassembler    <-- SDK: per-flow TCP byte stream, ordered
       |
       v
Frame/message parser   <-- yours: length prefix / delimiter / magic
       |
       v
Business/event decoder <-- yours: protocol semantics
       |
       v
event.Value
```

For example:

```go
type Message struct {
    Opcode uint16
    Body   []byte
}

func parseFrames(payload []byte) ([]Message, error) {
    // framing only
}

func decodeMessage(msg Message) (eventType, schemaID string, value event.Value, err error) {
    // protocol semantics only
}
```

This makes malformed framing easier to diagnose and keeps protocol semantics independent from packet boundaries.

## 6. Do not assume one request equals one message

`DecodeRequest` is a capture/decode input, not automatically a logical game message.

Depending on the capture boundary, one input may contain:

```text
one input -> zero messages
one input -> one message
one input -> multiple messages
```

A stream protocol **will** split one logical message across multiple inputs. The pipeline
performs no reassembly — it hands you one captured packet at a time.

The classic symptom: an HTTP decoder that parses the request line and headers correctly
but reports an empty body on every request, because the body arrived in the next TCP
segment. Decoding each packet independently loses it silently.

Use the SDK reassembler instead of writing your own buffer map:

```go
var ra = framing.NewReassembler()   // process-lifetime, safe for concurrent use

seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
if !ok {
    return done(stream, req)
}
stream := ra.Push(seg)              // ordered bytes accumulated for seg.Flow
for {
    msg, n := parseOneMessage(stream.Bytes())
    if n == 0 {
        break                        // incomplete: wait for the next segment
    }
    stream.Consume(n)
    emit(msg)
}
```

`Reassembler` handles out-of-order segments, retransmissions and duplicate data, and
drops per-flow state on FIN/RST. If your protocol is datagram-based (UDP), skip it —
`ExtractL7` alone is sufficient.

## 7. Event output workflow

For every logical message:

```text
Message
  |
  +--> business fields ----> event.Value ----> Draft.Value   -> payload_msgpack
  +--> network facts ------> event.Value ----> Draft.Meta    -> meta_msgpack
  +--> analysis data ------> event.Value ----> Draft.Analysis -> analysis_msgpack
  |
  v
DecodeResponseV2(done=false)
```

Since v0.8.0 the three channels are separate (§7.1). `event.Draft` is the preferred
builder — it marshals each channel and echoes `input_id`:

```go
draft := event.Draft{
    Type:           eventType,
    Value:          businessValue,          // required, root must be object
    Meta:           metaValue,              // optional; omitted when null
    Analysis:       analysisValue,          // optional; omitted when null
    CorrelationKey: flowKey.Canonical(),
}
resp, err := draft.ToResponse(req.GetInputId())
if err != nil { /* send error + done */ }
stream.Send(resp)
```

Older plugins put `_meta` / `_state_changes` / `direction` at the top level of the payload;
`event.SplitReservedKeys` still splits them out. Do not do this in new code.

After all events for the input:

```text
DecodeResponseV2(done=true)
```

A plugin that forgets the completion response can leave the pipeline waiting for the decode operation to finish.

## 8. `event.Value` usage

Use SDK conversion helpers rather than inventing a parallel representation.

```go
value := event.ValueFromAny(map[string]any{
    "player_id": "p001",
    "opcode":    42,
    "online":    true,
})

payload, err := value.MarshalMsgpack()
if err != nil {
    return fmt.Errorf("marshal event payload: %w", err)
}
```

For JSON:

```go
value, err := event.ValueFromJSON(rawJSON)
if err != nil {
    return fmt.Errorf("decode JSON payload: %w", err)
}
```

Remember that accessors return `(value, ok)` where applicable:

```go
s, ok := value.AsString()
if !ok {
    // wrong Value kind
}
```

Do not treat `String` as a field or assume `AsObject()` returns only one value.

## 9. Build the smallest working plugin first

Recommended sequence:

```text
1. plugin.yaml validates
2. plugin builds
3. process starts
4. decoder endpoint starts
5. registry registration succeeds
6. one known fixture decodes
7. event payload round-trips through UnmarshalValueMsgpack
8. only then add protocol complexity
```

This prevents protocol bugs from being mixed with registration/runtime bugs.

## 10. Test with fixtures, not live traffic only

Every protocol discovery should produce a deterministic fixture.

Recommended test data:

```text
fixtures/
├── login_request.bin
├── login_response.bin
├── truncated_header.bin
├── truncated_body.bin
└── unknown_opcode.bin
```

Tests should call the protocol parser directly where possible. Keep integration tests for the gRPC/registration boundary.

A corrected decoder should be validated against replayed fixtures. Do not rely on restarting a plugin and looking only at new live traffic.

## 11. TLS and opaque traffic

If the L7 payload is encrypted, the decoder cannot infer application fields merely from the ciphertext.

A decoder should distinguish:

```text
unsupported/unknown protocol
        !=
valid encrypted traffic
        !=
malformed packet
```

When a TLS handshake or TLS application record is detected but plaintext is unavailable, preserve enough evidence in logs/tests to establish that the input is valid encrypted traffic rather than incorrectly classifying it as a malformed game message.

Do not invent decoded business events from encrypted bytes.

## 12. Unknown-event strategy

If a plugin cannot identify a message:

- do not fabricate a business event;
- preserve the input identity/context;
- use an explicit unknown/forensics event only if that event is part of the plugin contract;
- record enough diagnostic information to determine whether the problem is framing, encryption, protocol version, or genuinely unknown data.

When **all** events are unknown, check in this order:

```text
1. payload framing
2. first 16-32 payload bytes
3. protocol/link metadata
4. TLS/encryption
5. capture noise / wrong flow
6. parser version/magic
7. replay fixture
```

Do not immediately rewrite the business decoder.

## 13. Correlation and causation

Only populate `correlation_key` or `causation_input_id` when the relation is actually known.

Do not use:

```text
packet_id as correlation_key
flow_id as causation_input_id
random UUID as business correlation
```

unless the protocol/domain contract explicitly defines that mapping.

## 14. Event schema discipline

Choose stable event names and explicit schema versions:

```text
Event type: game.login
Schema:     game.login.v1
```

Do not create dozens of event types merely to encode field variations.

When the payload shape changes incompatibly, create a new schema version.

## 15. State projection (`_state_changes`)

The GameTrace core semantic contract deliberately does **not** define business state
(Player / Inventory / HP / Gold ...). That state lives in the plugin: every decoder
that observes mutable entity state must declare it through the reserved `_state_changes`
array in the **Analysis channel** (`Draft.Analysis`). The host extracts `_state_changes`
via `event.ExtractStateChanges` and projects it into the
`state_changes` table — this is the **only** input to entity/state-change analysis. A
plugin that never emits `_state_changes` still registers and decodes fine, but its events
cannot participate in state-change projection.

```go
analysis := event.ValueFromMap(map[string]any{
    "_state_changes": []any{
        map[string]any{
            "subject_type": "player",
            "subject_id":   "p001",
            "op":           "set",
            "path":         "hp",
            "after":        100, // omit `before` for new entities; set both when known
        },
    },
})
draft := event.Draft{Type: "game.player.update",
    Value: businessValue, Analysis: analysis}
```

> 旧写法把 `_state_changes` 放在业务 payload 顶层，宿主仍会拆分兼容，但新插件应走
> `Draft.Analysis`。

Contract for each item (see `contract.yaml` `reserved_payload_fields._state_changes`):

- `subject_type` (required): entity kind, e.g. `player`, `item`, `session`. Meaning is
  defined by the plugin, not by GameTrace.
- `subject_id` (required): stable entity identifier within the session.
- `op` (required): one of `set`, `delete`, `merge`.
- `path` (required): dotted path to the mutated field, e.g. `hp`, `inventory.sword`.
- `before` / `after` (optional): prior / new value; omit what does not apply.
- `version` (optional, int): optimistic-concurrency version of the entity.
- `metadata` (optional): free-form context the plugin wants to attach.

The host validates each item; an item missing a required field or with an unknown
`op` is skipped with a warning and does not abort the write. Do not fabricate
`_state_changes` for events that do not actually mutate state — empty state is fine.

## 16. Pre-commit checklist

Before considering a decoder complete:

- [ ] `plugin.yaml` validates (`ValidateManifest` + `contract.NewPluginChecker().Check`).
- [ ] `go test ./...` passes.
- [ ] decoder never panics on malformed bytes.
- [ ] `DecodeRequest.payload` is stripped by `framing.ExtractL7` according to `link_type`
      (it is a **full link-layer frame**, not L7 — see §2).
- [ ] real payload framing was inspected.
- [ ] `Reassembler.Forget`/`Reset` is used where a capture run ends or a flow closes.
- [ ] `event.Value` is used for event payloads.
- [ ] `payload_msgpack` is produced with `MarshalMsgpack()`.
- [ ] business / meta / analysis stay in their own channels (`Draft.Value` / `Meta` / `Analysis`).
- [ ] every input eventually gets `done=true`.
- [ ] `input_id` is preserved in every response.
- [ ] unknown/encrypted traffic is not fabricated into business events.
- [ ] at least one real frame fixture exists **per link_type** (loopback + ethernet).
- [ ] the event payload round-trips through `event.UnmarshalValueMsgpack`.
- [ ] registration was verified against the intended registry.
- [ ] mutable entity state is declared via `_state_changes` when the plugin tracks it.
- [ ] `semantic_rules` pass the registration-time checker (`gt.semantic.*`).

## 17. What to ask the user instead of guessing

Stop and ask for clarification when any of these are unknown and materially affect correctness:

- the actual application protocol framing;
- whether a message is encrypted/compressed;
- the meaning of an opcode or field that cannot be inferred safely;
- the expected event type/schema ID when no existing convention is available;
- whether upstream reassembles TCP/application messages;
- whether a protocol relation is truly causal or merely correlated.

It is better to ask for one real payload fixture or protocol specification than to implement a parser based on a guessed frame layout.
