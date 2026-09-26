# Case Study: godot-tiny-mmo Decoder Boundary Failure

## Background

`godot-tiny-mmo` was the first real decoder integration case that exposed a contract problem in the SDK.

The failure was not caused by the game protocol parser itself. The root problem was an incorrect assumption about the boundary between capture data and decoder input.

## Original assumption

A decoder implementation assumed:

```text
DecodeRequest.payload
        |
        v
L7 application message
```

This assumption is unsafe.

A capture pipeline may provide data represented at different layers depending on capture source and configuration.

## Root cause

The SDK did not clearly separate these concepts:

```text
capture representation
        |
        v
transport framing
        |
        v
stream data
        |
        v
application message
        |
        v
business event
```

As a result, plugins could:

- treat packet bytes as application messages;
- ignore link type context;
- fail on TCP segmentation;
- confuse packet boundaries with protocol message boundaries.

## Correct decoder model

The decoder pipeline should reason in stages:

```text
Packet

DecodeRequest #1
DecodeRequest #2
DecodeRequest #3

        |
        v

Stream

(flow_id + direction)

        |
        v

Message

HTTP Request
WebSocket Frame
Game Packet

        |
        v

Event
```

## Contract lessons

### 1. Payload boundary must be explicit

A plugin MUST determine what layer `payload` represents.

It MUST NOT assume:

```text
payload == L7
```

without verifying the upstream contract.

### 2. link_type is context

`link_type` describes capture representation.

It is not a direct instruction to parse offsets or identify business protocols.

### 3. Packet is not Message

TCP, UDP and capture systems operate at different boundaries.

A decoder that needs application messages may need stream reassembly before decoding.

## SDK v1.1 response

The SDK documentation now defines:

- link type semantics;
- stream reassembly responsibility;
- runtime connection model;
- decoder development workflow.

The goal is that future AI-generated plugins understand data boundaries before writing protocol code.

## Principle

> Decoder SDK correctness depends more on data boundary contracts than on API shape.
