# GameTrace Event Model Design Specification

**Version:** v1.0 Draft
**Status:** Proposed
**Purpose:** Define the core event model used by GameTrace data pipeline, event storage, MCP query, replay, and analysis systems.

> ⚠️ 本文是设计稿，部分 DDL 已落后于实现（如缺 `context` 列、`origin_id` 索引）。
> 线上真实 schema 以 `pkg/store` 建表代码为唯一事实来源。

---

# 1. Overview

GameTrace uses an event-centric architecture.

The core principle:

> Event is the only data contract exchanged between system components.

All modules communicate through Event:

```
Capture
   |
   v
Event
   |
   +---- Decoder Operator
   |
   +---- Analyzer Operator
   |
   +---- AI Operator
   |
   +---- Storage
   |
   +---- MCP Query
```

Event is the single source of truth for all observable facts.

---

# 2. Design Goals

Event model must support:

* Network traffic analysis
* Game protocol decoding
* Client logs
* Automated testing events
* AI analysis results
* Replay
* Auditing
* Cross-module query
* Distributed processing

The design must satisfy:

1. Immutable
2. Append-only
3. Language independent
4. Schema evolvable
5. Replayable
6. Traceable

---

# 3. Core Principles

## 3.1 Event Represents Fact

Event represents something that has happened.

Examples:

```
network.packet.received

http.request.received

game.login.detected

battle.started
```

Event is not:

* command
* request
* RPC message
* mutable state

---

## 3.2 Event Is Immutable

After creation:

```
Identity
Trace
Context
Payload
Meta
Analysis
```

cannot be changed.

Forbidden:

```go
event.Type = "new_type"

event.Payload["key"] = value

event.CorrelationID = "new"
```

If new information is generated:

Create a new Event.

---

## 3.3 Event Has No Lifecycle State

Event itself has no status.

Forbidden:

```
CREATED
PROCESSING
COMPLETED
FAILED
```

Reason:

* violates immutability
* introduces concurrency problems
* harms replay

Lifecycle belongs to Operator execution.

---

# 4. Event Structure

The Event consists of six parts:

```
Event

├── Identity
│
├── Trace
│
├── Context
│
├── Payload
│
├── Meta
│
└── Analysis
```

核心不变式：**Payload ≠ Meta ≠ Analysis**（三段分离）。Payload 只承载纯业务字段；
Meta 承载系统附加的元信息（direction/msg_name/role/is_push 等）；Analysis 承载平台
推导/投影的数据（_state_changes/entity 等）。三者从模型层强制分离，前端/MCP/分析
全部依赖该分离，任何模块都不得把平台推导的东西塞回 Payload。

---

# 5. Identity

Identity answers:

> What is this Event?

```go
type Event struct {
    Identity Identity
    Relation Relation
    Payload Payload
}
```

## Identity Definition

```go
type Identity struct {

    // globally unique identifier
    ID EventID


    // capture or analysis session
    SessionID string


    // event type
    Type EventType


    // creator
    Source SourceID


    // event occurrence time
    Timestamp time.Time
}
```

---

## 5.1 Event ID

Event ID uses UUIDv7.

Reasons:

* globally unique
* distributed generation
* time sortable
* SQLite friendly

Definition:

```go
type EventID string
```

Rules:

* generated when Event is created
* never changed
* never reused

---

## 5.2 Event Type

Type uses string.

Examples:

```
network.packet

http.request

protobuf.message

game.login

battle.start
```

Reason:

* extensible
* plugins can define new types
* no core enum upgrade required

---

## 5.3 Schema ID

Event type and schema are separated.

Example:

```
Type:

game.login


Schema:

game.login.v1
```

Schema changes create new versions.

Never modify existing schema.

---

# 6. Relation

Relation answers:

> Where does this Event come from?

The model follows Event Sourcing concepts.

```go
type Relation struct {

    // direct cause event
    CausationID EventID


    // same business context
    CorrelationID string


    // original source event
    OriginID EventID
}
```

---

# 6.1 CausationID

Represents direct cause.

Example:

```
network.packet

        |
        v

http.request

        |
        v

game.login
```

Relations:

```
http.request.CausationID
=
network.packet.ID


game.login.CausationID
=
http.request.ID
```

---

# 6.2 CorrelationID

Represents the same business process.

Example:

```
CorrelationID = login-flow-001


packet
request
response
login-result
```

belong to the same flow.

Rules:

* created once
* immutable
* may branch into new contexts
* cannot be modified afterwards

Example:

```
capture-session

       |
       +---- login-flow
       |
       +---- battle-flow
```

---

# 6.3 OriginID

Represents the original input source.

Example:

```
packet

 |

http.request

 |

login

 |

anti-cheat.result
```

All derived events:

```
OriginID = packet.ID
```

Purpose:

* fast trace back
* replay
* impact analysis

---

# 7. Payload

Payload contains the event's business data.

Structure:

```go
type Payload struct {

    Value Value
}
```

## 7.1 Payload / Meta / Analysis 三段分离

事件除 `Payload`（纯业务字段）外，还有两个与 Payload 同级的可选部分（对应实现中的
`Event.Meta` / `Event.Analysis`，均为 `Value`）：

```
Payload   纯业务字段，如 {playerId, x, y}                     → 传输载体 payload_msgpack
Meta      元信息：direction、msg_name、role、is_push 等系统附加字段 → 传输载体 meta_msgpack
Analysis  分析数据：_state_changes、entity、entity_type、
           entity_id、change_count 等平台投影所需数据           → 传输载体 analysis_msgpack
```

规则：

- 插件侧通过 `event.Draft{Value, Meta, Analysis, CorrelationKey, CausationInputID}` 构造，
  `Draft.ToResponse` 负责编码为 `DecodeResponseV2` 三段。
- 宿主存储时三段合并为扁平 MsgPack 落 `events.payload`（旧数据兼容）；读取时按保留键
  （`_meta`、`_state_changes`、`entity*`、`change_count`）拆分回填 `Meta`/`Analysis`。
- 旧插件未上报 Meta/Analysis 时，宿主从扁平 payload 自动拆分兜底。

---

# 8. Value Model

Value replaces:

```go
map[string]interface{}
```

and

```go
any
```

because they are weakly typed and difficult across languages.

---

## Value Definition

```go
type Value struct {

    Kind ValueKind


    Bool bool

    Int int64

    Uint uint64

    Float float64

    String string

    Bytes []byte


    Array []Value


    Object map[string]Value
}
```

---

## Value Types

```go
type ValueKind int

const (

    Null ValueKind = iota

    Bool

    Int

    Uint

    Float

    String

    Bytes

    Array

    Object
)
```

---

# 9. Binary Data Handling

Large binary data should not be stored directly in Event.

Examples:

* raw packet dump
* screenshots
* large logs
* compressed data

Small data:

```
Value.Bytes
```

Large data:

```
Blob Reference
```

Example:

```go
type BlobRef struct {

    ID string

    Size int64

    ContentType string
}
```

---

# 10. Operator Model

Operators transform Events.

Definition:

```
Events -> Events
```

Interface:

```go
type Operator interface {

    Process(
        ctx context.Context,
        events []*Event,
    ) ([]*Event,error)

}
```

---

Example:

## Capture Operator

Input:

```
external world
```

Output:

```
network.packet Event
```

---

## Decode Operator

Input:

```
network.packet
```

Output:

```
http.request
```

---

## AI Operator

Input:

```
game.events
```

Output:

```
security.analysis
```

---

# 11. Operator Execution

Execution state belongs to Operator.

Not Event.

Example:

```
Event

    |
    v

Operator Run

    |
    v

New Event
```

Future storage:

```
operator_runs

operator_inputs

operator_outputs
```

---

# 12. Error Handling

Errors are Events.

Do not modify Event status.

Example:

Input:

```
network.packet
```

Decoder fails.

Create:

```
system.error
```

Relation:

```
CausationID = packet.ID
```

Payload:

```json
{
    "operator":"http.decoder",
    "error":"invalid packet"
}
```

---

# 13. Event Store Principles

Event Store uses:

* SQLite
* Append Only
* Event as Source of Truth

Rules:

Allowed:

```
INSERT Event
```

Forbidden:

```
UPDATE Event
DELETE Event
```

---

# 14. Event Table

```sql
CREATE TABLE events (

    id TEXT PRIMARY KEY,


    session_id TEXT NOT NULL,


    type TEXT NOT NULL,


    source TEXT NOT NULL,


    timestamp INTEGER NOT NULL,


    causation_id TEXT,


    correlation_id TEXT,


    origin_id TEXT,


    context BLOB NOT NULL,


    payload BLOB NOT NULL,


    created_at INTEGER NOT NULL
);
```

---

# 15. Payload Storage

Default:

```
Value

   |

MsgPack Encode

   |

SQLite BLOB
```

Reasons:

* compact
* fast
* language independent

JSON is only a debug representation.

---

# 16. Query Index

Recommended indexes:

```sql
CREATE INDEX idx_events_time
ON events(timestamp);


CREATE INDEX idx_events_session
ON events(session_id);


CREATE INDEX idx_events_type
ON events(type);


CREATE INDEX idx_events_correlation
ON events(correlation_id);


CREATE INDEX idx_events_origin
ON events(origin_id);


CREATE INDEX idx_events_causation
ON events(causation_id);
```

---

# 17. Final Event Model

```
                            Event

             ┌──────────────┼──────────────┐
             │              │              │
        Identity         Trace          Context
             │              │              │
        ID EventID    CausationID    (网络上下文)
        SessionID     CorrelationID
        Type          OriginID
        Source
        Timestamp

             ├──────────────┼──────────────┐
             │              │              │
        Payload          Meta           Analysis
             │              │              │
        Value           direction      _state_changes
     (纯业务字段)       msg_name       entity / entity_type
                       role           entity_id / change_count
                       is_push
```

---

# 18. Future Extensions

Possible future additions:

* Event Tags
* Event Metrics
* Multi-Causation Relationship
* Event Graph Query
* Materialized Views
* Vector Embedding
* AI Reasoning Trace

These should be extensions and must not violate the immutable Event core model.

---

# Conclusion

GameTrace Event Model is based on:

* Event Sourcing principles
* Immutable facts
* Append-only storage
* Schema separated payload
* Operator-based derivation

The core contract is:

```
Event = Identity + Trace + Context + Payload + Meta + Analysis
```

其中 **Payload ≠ Meta ≠ Analysis**（三段分离）：Payload 为纯业务字段，Meta 为系统元信息，
Analysis 为平台推导/投影数据。任何模块都不得把后两者塞回 Payload。

All future modules should be designed around this model.

```
```
