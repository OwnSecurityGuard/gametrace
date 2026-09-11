import { describe, expect, it } from "vitest";
import { fuzzyTokens, haystackIncludes, eventMatchesQuery, changeMatchesQuery } from "./fuzzy";
import type { DecodedEvent } from "@/types/event";
import type { Change } from "@/types/state-change";

describe("fuzzyTokens", () => {
  it("拆出小写关键词", () => {
    expect(fuzzyTokens("Player Move")).toEqual(["player", "move"]);
    expect(fuzzyTokens("  HTTP Request  ")).toEqual(["http", "request"]);
    expect(fuzzyTokens("")).toEqual([]);
    expect(fuzzyTokens("   ")).toEqual([]);
  });
});

describe("haystackIncludes", () => {
  it("所有关键词都命中才为真（AND）", () => {
    expect(haystackIncludes("player moved to position", ["player", "position"])).toBe(true);
    expect(haystackIncludes("player moved", ["player", "position"])).toBe(false);
  });
  it("空关键词恒为真", () => {
    expect(haystackIncludes("anything", [])).toBe(true);
  });
});

const event: DecodedEvent = {
  id: "evt-123",
  timestamp: "2023-01-01T00:00:00Z",
  session_id: "s1",
  protocol: "game",
  raw_len: 100,
  correlation_id: "corr-9",
  data: { playerId: "1001", action: "move", target: { x: 1, y: 2 } },
  meta: { direction: "client_to_server", msg_name: "PlayerMove", role: "" },
};

describe("eventMatchesQuery", () => {
  it("匹配协议名", () => {
    expect(eventMatchesQuery(event, "game")).toBe(true);
  });
  it("匹配消息名（大小写不敏感）", () => {
    expect(eventMatchesQuery(event, "playermove")).toBe(true);
    expect(eventMatchesQuery(event, "PLAYERMOVE")).toBe(true);
  });
  it("匹配方向", () => {
    expect(eventMatchesQuery(event, "client_to_server")).toBe(true);
  });
  it("匹配业务 payload 字段值", () => {
    expect(eventMatchesQuery(event, "1001")).toBe(true);
    expect(eventMatchesQuery(event, "move")).toBe(true);
  });
  it("多个关键词 AND 语义", () => {
    expect(eventMatchesQuery(event, "player move")).toBe(true);
    expect(eventMatchesQuery(event, "player attack")).toBe(false);
  });
  it("空查询恒匹配", () => {
    expect(eventMatchesQuery(event, "")).toBe(true);
  });
});

const change: Change = {
  id: "c1",
  seq: 1,
  event_id: "evt-123",
  timestamp: "2023-01-01T00:00:00Z",
  offset_ms: 100,
  subject_type: "player",
  subject_id: "1001",
  entity_key: "player:1001",
  op: "set",
  path: "position.x",
  before: 0,
  after: 10,
  version: 1,
  source: {
    event_id: "evt-123",
    timestamp: "2023-01-01T00:00:00Z",
    offset_ms: 100,
    msg_name: "PlayerMove",
    kind: "request",
    operation_key: "k",
  },
};

describe("changeMatchesQuery", () => {
  it("匹配字段路径", () => {
    expect(changeMatchesQuery(change, "position.x")).toBe(true);
  });
  it("匹配来源消息名", () => {
    expect(changeMatchesQuery(change, "PlayerMove")).toBe(true);
  });
  it("匹配实体键", () => {
    expect(changeMatchesQuery(change, "player:1001")).toBe(true);
  });
  it("匹配变更后值", () => {
    expect(changeMatchesQuery(change, "10")).toBe(true);
  });
  it("空查询恒匹配", () => {
    expect(changeMatchesQuery(change, "")).toBe(true);
  });
});