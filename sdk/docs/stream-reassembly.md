# Stream Reassembly

## DecodeRequest granularity

`DecodeRequest` is packet/capture record level. It is NOT guaranteed to be message level.

```
Packet -> DecodeRequest #1 / #2 / #3 -> (reassemble) Stream -> Message
                                              ^
                                     flow_id + direction
```

The pipeline performs no reassembly: it hands you one captured packet at a time. A decoder
must not assume one capture input equals one complete application message.

The classic symptom: an HTTP decoder parses the request line and headers correctly but
reports an empty body on every request, because the body arrived in the next TCP segment.

## Use `framing.Reassembler`

```go
type decoder struct {
    reasm *framing.Reassembler   // process-lifetime: reassembly is inherently cross-packet
    // ...per-flow counters
}

seg, ok := framing.ExtractL7(req.GetPayload(), req.GetLinkType())
if !ok {
    return stream.Send(event.Done(req.GetInputId()))
}

st := d.reasm.Push(seg)          // ordered bytes accumulated for seg.Flow
for {
    buf := st.Bytes()
    if len(buf) == 0 || len(buf) > maxStreamBytes {
        break
    }
    msg, n, ok := parseOneMessage(buf)
    if !ok {
        break                     // incomplete: wait for the next segment
    }
    emit(msg)
    st.Consume(n)
}
```

- Push **every** segment, including empty payloads: SYN / ACK / FIN flags drive per-flow
  state inside the reassembler.
- UDP and proxy-class inputs (`1001` / `1002`) do not participate in reassembly.
- Cap per-flow memory (`maxStreamBytes`, the reference plugin uses 4 MiB) and drop the
  buffer when a flow never parses — otherwise an unparseable flow grows without bound.

## Lifecycle: `Forget` and `Reset`

```go
ra.Forget(key)   // drop one flow's state without waiting for FIN
ra.Reset()       // clear ALL reassembly state
```

The reassembler records, per flow and direction, how far the sequence space has been
consumed. That state survives as long as the process does.

**Reset between capture runs.** Reusing one `Reassembler` across unrelated captures without
a `Reset()` lets stale sequence state corrupt the next run.

## The statefulness trap (most common misdiagnosis)

Because consumed positions are remembered, replaying the same packets against a live plugin
process yields nothing the second time: every segment is treated as a duplicate retransmission
and dropped.

```
1st pass (fresh Reassembler): 300 events
2nd pass (same Reassembler):    0 events   <- decoded=0 AND decode_errors=0
```

Judgement rule: **`decode_errors == 0` but `decoded == 0` is not a plugin bug** — the stream
state was already consumed. Restart the plugin process before each verification run, and run
one verification command per process.

## Ordering, retransmission, loss

The reassembler handles out-of-order segments, retransmissions and duplicate data, and drops
per-flow state on FIN/RST. It does not fabricate missing bytes: a genuinely lost segment
leaves a hole, and the parser simply sees an incomplete buffer and waits. Your parser must
therefore tolerate "incomplete, try again later" as a normal outcome, not an error.
