# Decoder Contract v1.1 — 已废止

> **v1.1 契约已废止。** 当前线上契约是 `gt.decoder/v2`（`contract/contract.yaml`，
> `spec_version: 6`，SDK v0.8.2）。本文件仅作历史存档保留。

v1 → v2 的关键差异（写新插件时不要参考 v1 的任何一条）：

| 项 | v1.1 | v2（当前） |
|---|---|---|
| 事件产出 | JSON 字符串，业务字段放 `data` 子对象、平台语义放 `_fields` | tagged MsgPack 的 `event.Value`，业务字段挂根对象 |
| 产出字段 | `payload`（JSON） | `payload_msgpack` + `meta_msgpack` + `analysis_msgpack`（v0.8.0 起三段分离） |
| 解码接口 | `Decode`（v1） | `DecodeV2`（v1 解码接口已移除） |
| payload 边界 | 本文件曾主张 "payload 已是 L7，不要剥头" | **错误**。pcap 来源 payload 是完整链路层帧，必须按 `link_type` 用 `framing.ExtractL7` 剥头 |
| manifest | v1 结构 | `gt.decoder/v2`；`schemas[]` + `semantic_rules[]` 两层契约 |

本文件唯一仍然成立的历史结论是 **payload 边界必须显式**（正是这条结论催生了
`payload-framing-by-link-type` 规则）；该结论的完整现代表述见
[../Agents.md §8.1](../Agents.md) 与 `contract/contract.yaml` 的 `payload_framing` 段，
案例背景见 [case-study-godot-tiny-mmo.md](./case-study-godot-tiny-mmo.md)。
