<div align="center">

<img src="web/public/logo.png" alt="GameTrace mascot" width="128" />

# GameTrace

**Turn game network traffic into structured debugging evidence.**

Capture → decode with a plugin → request/response pairs, protocol errors, field-level state changes
→ inspect in the Web UI or query it over MCP.

[**English**](README.md) · [简体中文](README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<!-- TODO: demo GIF — one take: start a capture → play the game → decoded events stream in →
     expand one request/response pair → state diff hp 100 → 65 → an agent queries and summarises.
     Drop the file at docs/demo.gif. -->

</div>

GameTrace captures live or recorded game traffic, decodes custom and proprietary protocols through
independent **decoder plugins**, and turns packets into a **queryable trace**: protocol events,
request/response pairs, server pushes, protocol errors, causal chains and entity state changes.
Humans inspect that trace in a Web dashboard; automation and AI agents query the same trace over MCP.

It is built for **game QA, test automation, game development, protocol analysis and AI agents**.

## What problem does GameTrace solve?

Game testing has a gap between what happens on screen and what happens on the network.

- A QA engineer reproduces a problem from the UI, but has no view of the requests underneath it.
- A UI automation test fails, and the result does not say whether the client sent the wrong request,
  got an error response, missed a server push, or failed to apply a state update.
- A test or load-testing system needs realistic game protocol data, and getting it means separate
  packet-capture plus protocol-analysis work every time.

GameTrace closes that gap by turning game traffic into a trace that a human can inspect, automation
can consume and an AI agent can query — with protocol semantics attached.

## Use cases

### 1. Real protocol data for test automation and load testing

Capture actual gameplay traffic and decode it into structured protocol data that API/protocol
automation, load and stress runs, regression suites, replay and scenario generation, and test-data
preparation can reuse. GameTrace supplies the observed protocol data and semantics; your load
generator or automation framework stays independent.

### 2. Network visibility for QA — and rules that flag what looks wrong

Capture runs in parallel with the normal test workflow — QA keeps playing and reporting as before.
When something looks wrong, the session already holds which messages were sent, how requests pair
with responses, what the server pushed, which decodes failed, and every field-level state change in
between. A UI-level symptom becomes a protocol-level trace.

QA does not have to read that trace passively. A project carries **check rules**: a condition over the
decoded fields of an event, plus the wording to show when it hits.

```json
{
  "id": "game.item.grant_without_reason",
  "name": "item grant without reason",
  "enabled": true,
  "when": [
    { "path": "_meta.msg_name", "op": "eq", "value": "item.grant" },
    { "path": "data.reason", "op": "not_exists" }
  ],
  "title": "Item granted with no reason field",
  "cooldown_sec": 60
}
```

A rule is one GJSON `path` + one op from a closed set (`eq` · `neq` · `exists` · `gt` · `in` ·
`contains` · `prefix` …), combined with an implicit `all` or an explicit `all`/`any`. The pipeline
evaluates the project's rules on every decoded event; a hit is written to the session's **alerts**
(命中提醒) view with the triggering message and the packets just before and after it, and pops a
desktop notification on the probe machine QA is sitting at. So "this buff should never arrive without
a request" becomes a rule instead of a hunch — noticed during the capture, not after the report.

Rules are authored in the project's 检查规则 form or by an agent via `set_project_rules` (project
admin; an invalid rule rejects the whole write), and are snapshotted when a session starts, so a change
takes effect on the next capture. Up to 50 rules per project; a per-rule cooldown (30 s by default)
keeps one repeating condition from flooding.

### 3. Network evidence for UI automation failures

When a client-side UI test fails, GameTrace is an independent evidence source for the same time
window: the request that triggered the operation, its response, asynchronous pushes, protocol errors
and the resulting state changes. That separates "the UI assertion is wrong" from "client-server
communication or state synchronisation failed".

### 4. Protocol analysis and AI-assisted debugging

Capture and replay traffic for custom protocols, with protocol knowledge living in standalone decoder
plugins. The resulting trace is exposed through the Web UI and MCP, so an agent can inspect protocol
behaviour, find related requests and responses, trace state changes, analyse decoding failures, and
develop and verify decoder plugins itself.

## What GameTrace produces

```text
Live traffic / PCAP
        │
        ▼
   Packet capture        (per-port pcap on a local or remote probe, or .pcap replay)
        │
        ▼
   Decoder plugin        (independent gRPC process; one Go binary + one plugin.yaml)
        │
        ▼
 Structured protocol events
        ├── requests / responses
        ├── server pushes
        ├── protocol errors
        ├── rule hits → alerts      (project check rules, per decoded event)
        ├── request/response pairs        (causation = parent, correlation = conversation)
        ├── causal relationships          (OpenTelemetry-style TraceContext)
        └── entity state changes          (e.g. Player:1001.hp: 100 → 65)
        │
        ▼
   Web UI · MCP · automation
```

That trace is usable as a debugging evidence source, a protocol analysis dataset, a test-automation
data source, a load-testing scenario source, and an input for AI-assisted analysis.

The point is not to store packets — it is the chain
**packet → protocol event → pair → state change → trace → agent query**.

## What GameTrace is — and is not

GameTrace **is**:

- a game traffic capture and replay layer (local, or pushed from a remote probe machine)
- a protocol decoding framework — protocol knowledge ships as plugins, not in the core
- a structured game-network trace with pairing, causality and state diffs
- a debugging evidence source for humans and automation
- an MCP surface so AI agents work on the same evidence

GameTrace is **not**:

- a load-testing engine or a stress-run driver
- a UI automation framework, or a generic API testing framework
- a generic packet sniffer that stops at bytes
- a protocol database that already understands every game

## How it works

Three pieces, deliberately separated:

1. **`gt-agent` (probe)** runs on the machine that has the game. It captures per-port pcap, or takes
   mobile traffic through a proxy lease, and pushes raw frames to the server over gRPC. It can also
   tunnel locally running decoder plugins to the registry, so plugins never leave your machine.
2. **`gt-pipeline` (runtime plane)** owns capture sessions, dispatches bytes to plugins, evaluates the
   project's check rules on each decoded event, projects results into pairs, state changes, alerts and
   an event index, and persists to SQLite.
3. **`gt-mcp` (control plane)** is a pure adapter: it exposes everything — capture, query, plugins,
   projects, probes — as MCP tools and serves the Web UI. It spawns no subprocesses, writes no files
   and does no failure attribution.

Protocol knowledge is never in the core: **GameTrace is the pipeline, not a protocol encyclopedia.** A
plugin decides what a byte range means; the platform stores no plugin source, compiles nothing and
launches nothing.

## Demo

A one-take GIF belongs here: start a capture → play the game → decoded events stream in → expand one
request/response pair → `hp: 100 → 65` → an agent queries it and summarises. Not recorded yet —
[Quick start](#quick-start) gets you the same result in five minutes.

## Quick start

### A. See the value in five minutes — no game, no hardware

```bash
# synthetic probe + real decoder plugin through the real pipeline, then serve the Web UI
SIM_SERVE=1 go test ./cmd/gt-pipeline/ -run TestSimulate -v
```

Open `http://127.0.0.1:8781/` — if `.env` at the repo root sets `GT_AUTH_TOKENS`, log in with the
first identity there; otherwise the UI opens straight into the data. Either way the run replays a
synthetic game session through a real decoder plugin and lands hundreds of field-level state changes
over a hundred-plus entities. The **protocol events** (协议事件) view shows request/response pairs
side by side; **state changes** (状态变更) shows before/after per field. Press Ctrl-C to stop.

> The Web UI is currently Chinese-only (an English pass is on the Roadmap).

### B. Capture your own traffic (QA path)

> Requires a server your admin already deployed — [team deployment](docs/team-deployment.md).
> On the machine you will capture on: install [Npcap](https://npcap.com/) on Windows (tick
> "WinPcap API-compatible Mode"), or `sudo apt install libpcap-dev` on Linux.

1. **Sign in** — open `http://<server>:<GT_MCP_PORT>` (compose default host port `18781`) and log in
   with the token your admin issued; or self-register from "没有令牌？快速开始".
2. **Attach that machine** — "接入设备" → pick the OS → download `gt-agent`, unzip and run it. No
   token, address or session id to fill in: the zip carries the back-connect config
   (`config.embedded.json`) and the probe registers into your team on startup. Missing platform
   builds can be compiled on demand from the download page.
3. **Pick the game port** — once the device shows online, click "开始抓包": choose the machine, the
   game port (e.g. `9250`) and the decoder plugin. Ports and plugins are chosen per capture, not per
   probe.
4. **Capture and reproduce** — play normally. Switching port or plugin is another "开始抓包", no
   re-attach needed.
5. **Read the protocol behaviour** — stop the capture and open the session: each row in 协议事件 is
   one decoded message (msg_name / direction / semantic tags / field summary), and a row with a pair
   badge expands into request on the left, response on the right. 状态变更 shows before/after by
   operation, entity and time. 命中提醒 lists what the project's check rules flagged during the
   capture, each hit carrying the messages around it.

Nothing showing up? Check [member onboarding · troubleshooting](docs/member-onboarding.md#4-常见问题)
(port match, firewall on `9091/9092`, Npcap/libpcap installed).

### C. Run from source (developer path)

**Prereqs:** Go 1.26+ · libpcap (Linux) / [Npcap](https://npcap.com/) (Windows)

```bash
make build        # gt-mcp + gt-pipeline + gt-singbox-agent → bin/

# terminal 1 — runtime plane (data path; start first)
./bin/gt-pipeline.exe -workdir . -log-format text

# terminal 2 — control plane
./bin/gt-mcp.exe -work-dir . -log-format text

# terminal 3 — probe on this machine (the pipeline no longer captures locally;
# all raw frames arrive through agent ingest :9092)
go run ./cmd/gt-agent
```

Point an MCP client at `http://127.0.0.1:8781/mcp` (Streamable HTTP) or
`http://127.0.0.1:8781/sse` (SSE), then smoke-test the stack:

```
start_capture(port=8984)      # session starts; keep the session_id
#   … generate traffic: go run ./examples/http/server  (+ ./examples/http/client)
get_session_status            # packets_in > 0 · decode_errors == 0
list_decoded_data(limit=50)   # decoded events — needs a decoder plugin, see Plugins below
```

`make test` runs the suite; `make run-mcp` / `make run-pipeline` use `go run` for fast iteration.

## For AI agents

GameTrace exposes capture, protocol analysis, debugging and plugin development through **MCP (Model
Context Protocol)** — agents work on the same evidence a human tester sees:

```text
Game Client
    │
    ▼
 GameTrace ── captured traffic · decoded events · request/response relations
    │         · state changes · protocol diagnostics
    ▼
 MCP Server  :8781  (/mcp · /sse)
    │
    ▼
 AI Agent
```

Typical agent workflows: investigate a failed game test from network evidence, inspect the messages
around a specific event, follow request → response → state change, analyse unknown or partially
decoded traffic, build and verify a decoder plugin, or generate scenarios from captured traffic.

Entry points, in this order:

| Tool | Why an agent calls it first |
|------|------------------------------|
| `get_capabilities` | Self-describing catalog of every tool grouped by workflow, plus recommended call chains |
| `read_skill` | The step-by-step agent skills under [`skills/`](skills/) (decoder plugin guide, protocol lineage analysis) |
| `sample_bytes_plugin` | Facts only — hexdump, length histogram, first-byte distribution of a session's traffic |
| `get_protocol_catalog` | Which named business protocols were actually observed, per direction |
| `query_state_changes` / `get_state_change_detail` | Field-level diffs and the protocol chain behind one change |
| `scaffold_plugin` → `connect_plugin` → `verify_plugin` → `explain_plugin` | The full plugin loop, with failure attribution built in |

`scaffold_plugin` returns template file contents and writes nothing: the platform has no plugin
directory, never compiles and never launches a plugin. Your agent writes the skeleton into its own
workspace, you build and run it locally, and `connect_plugin` tells the platform to look for it.
**Your plugin source stays yours.**

### MCP endpoint

```text
http://127.0.0.1:8781/mcp          # Streamable HTTP ( /sse for SSE transport )
Authorization: Bearer <token>      # required when the server sets GT_AUTH_TOKENS
```

## Plugins & protocol support

A plugin is **one Go binary + one `plugin.yaml`**: the manifest declares identity and contract
(`api_version`, `protocol`, `transports`, `semantic_rules`), the host validates it at registration
*and* per event at decode time. Plugins are independent gRPC processes: hot-load, hot-swap on a
running session (`set_session_plugin`), crash-isolated from the pipeline.

They depend only on [`github.com/OwnSecurityGuard/gametrace/sdk`](sdk/) — a separate Go module in
this monorepo with no coupling to internals. Guide: [`docs/gt-plugin-development.md`](docs/gt-plugin-development.md).

```
scaffold_plugin ──► build + run locally ──► connect_plugin ──► verify_plugin
                        │                        │                  │
                        └───────────── explain_plugin ──────────────┘
                        (attributes the most recent connect / verify failure)
```

No decoder for your protocol yet? That is expected — writing one is the extension point the whole
design is built around, and the loop above is agent-drivable.

### Examples

| Example | What it shows |
|---------|---------------|
| [`examples/http`](examples/http) | client + server generating parseable HTTP traffic (`:8984`) for a first end-to-end session |
| [`examples/http-decoder`](examples/http-decoder) | HTTP/1.1 framing (request/status line + JSON envelope semantics) |
| [`examples/ws-decoder`](examples/ws-decoder) | WebSocket over TCP: HTTP Upgrade handshake + RFC 6455 frames, direction from the MASK bit — with [`examples/ws`](examples/ws) (`:8990`) |
| [`examples/lp-decoder`](examples/lp-decoder) | length-prefix framing (1/2/4-byte fields, endianness varying per frame), direction derived from message identity — with [`examples/lp`](examples/lp) (`:8998`: login / inventory / item / resource scenarios whose pushes project to state diffs) |
| [http-stream-decoder](sdk/examples/http-stream-decoder) | reference decoder in the SDK module |
| [`sdk/docs/case-study-godot-tiny-mmo.md`](sdk/docs/case-study-godot-tiny-mmo.md) | case study: decoding the Godot debug protocol (request/response + state subjects) |

TCP framing differs per protocol, so the templates are references, not copy-paste: run
`sample_bytes_plugin` against real traffic before wiring a decoder to it.

## MCP tools

Every capability above is an MCP tool. The complete table is generated from the tool registrations
into [`docs/mcp-tools.md`](docs/mcp-tools.md) (CI fails on drift); `get_capabilities` returns the same
catalog at runtime, grouped as:

| Group | Representative tools |
|-------|---------------------|
| Capture & sessions | `start_capture` · `stop_capture` · `get_session_status` · `set_session_plugin` |
| Probes & mobile proxy | `probe_start_capture` · `probe_import_archive` · `create_proxy_lease` · `start_lease_capture` |
| Query the trace | `list_decoded_data` · `get_protocol_catalog` · `list_connections` · `list_session_alerts` · `query_decode_errors` |
| State analysis | `query_state_changes` · `get_state_change_detail` · `list_state_changes` |
| Plugin development & verification | `scaffold_plugin` · `connect_plugin` · `verify_plugin` · `explain_plugin` · `sample_bytes_plugin` |
| Projects & members | `create_project` · `set_project_plugins` · `add_project_member` · `move_session_to_project` |

**Agent self-check** — paste this to your agent for a one-breath full-stack verification:

```
1. tools/list / get_capabilities               → every group above is present
2. start_capture(port=8984, plugin=<yours>)    → keep session_id  (gt-agent must be running)
3. get_session_status                          → packets_in > 0, decode_errors == 0
4. list_decoded_data(limit=50)                 → event_type / schema_id / flat semantic fields
5. get_protocol_catalog(session_id=…)          → which business protocols were seen, per direction
6. query_state_changes                         → field-level before/after (e.g. hp: 100 → 65)
```

## Architecture

Two planes, split so **MCP stays a pure adapter**. Plugin *source, build and process* are not a
plane at all: they live on the user's machine and the platform only observes instances that register
themselves.

```
Team member host                    AI Agent (Claude / DeepSeek / …)
┌────────────────────────────────┐      │  MCP over HTTP — :8781  (/sse + /message · /mcp)
│  gt-agent                      │      │  Authorization: Bearer <token>
│  ├─ capture ingest ── :9092 ───┐     ▼
│  └─ plugin tunnel ─── :9091 ───┼──────────────────────────────────────┐
│                                │      Runtime Plane — gt-pipeline      │
│  decoder plugin                │      capture → decode → project       │
│  source / binary / process     │      → SQLite · PluginRegistry        │
│  (yours)                       │      :9091 · AgentIngest :9092        │
│                                │      · MCP adapter gt-mcp :8781       │
└────────────────────────────────┼─────┘                                │
                                 └──── gRPC register + heartbeat ────────┘
                                   (observed, never managed)
```

| Port | What listens |
|------|--------------|
| `:8781` | gt-mcp HTTP — `/sse` + `/message` (SSE) · `/mcp` (Streamable HTTP) · `/events/plugins` — Bearer-auth when `GT_AUTH_TOKENS` is set |
| `:9888` | CaptureControl gRPC — gt-mcp → gt-pipeline |
| `:9091` | PluginRegistry gRPC — decoder plugins → gt-pipeline (remote members register through the gt-agent tunnel) |
| `:9092` | AgentIngest gRPC — gt-agent pushes raw frames from a team member's machine |

## Team deployment

One shared server for the team via Docker Compose — three services (gt-pipeline + gt-mcp +
Postgres). Fill the five required variables in `.env` first (`GT_AUTH_TOKENS=alice=gt_xxx,bob=gt_yyy`
with an `:admin` suffix for admins, `GT_PUBLIC_HOST`, `GT_PUBLIC_REGISTRY_PORT`,
`GT_PUBLIC_INGEST_PORT`, `GT_LAN_IP`), then:

```bash
cp .env.example .env   # then fill the five required values (compose enforces them)
docker compose up -d --build
```

Host-published ports default to **`19888/19091/19092/18781`** (container-internal ports keep the
`9888/9091/9092/8781` numbering; see the offset note at the top of `docker-compose.yml`). Members
connect from their own machines with a single `gt-agent` binary that pushes captures to `:9092` and
hosts local decoder plugins through the registry tunnel (`GT_TUNNEL=1` is injected for them).

## Documentation

- [Team deployment guide](docs/team-deployment.md) (Chinese) · [Member onboarding](docs/member-onboarding.md) (Chinese)
- [Plugin development guide](docs/gt-plugin-development.md) — writing and verifying a decoder
- [MCP tool catalog](docs/mcp-tools.md) — generated from the tool registrations
- [Protocol event model](docs/event.md) — how decoded events, semantics and state changes are shaped
- [Agent skills](skills/) — `decoder-plugin-guide`, `protocol-lineage-analysis`, served via `read_skill`

## Contributing

```bash
make vet && make lint && make test     # the CI gate
make docs                              # regenerate docs/mcp-tools.md after adding an MCP tool
```

- Adding an MCP tool means adding it in `cmd/gt-mcp/` and re-running `make docs` — CI fails if the
  generated table drifts.
- Decoders and experiments live under `examples/` and `sdk/examples/`; the SDK is a separate module,
  so `make sdk-test` covers it.
- Keep the boundaries intact: `gt-mcp` stays a pure adapter, and nothing in the platform may store,
  compile or launch plugin source.

## Roadmap

- [ ] **Session timeline & replay views** — first-class timeline and replay on top of the existing
  capture / sessions / connections / protocol data / plugins / projects / probes coverage in `web/`
- [ ] **Replay & load-test codegen** — replay and load scripts generated by agents from a captured
  session trace (Scenario → Replay phases)
- [ ] **First-class read tools** for the `event_index` and `plugin_debug_access` audit tables
- [ ] **SDK advanced docs** — reassembly control (`Reassembler.Forget/Reset`), `MetaValue`,
  `FlowKey.Canonical`, plus more example decoders
- [ ] **English docs & UI** — the Web UI and the design docs are currently Chinese-only

## License

[MIT](LICENSE)
