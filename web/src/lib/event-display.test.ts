import { describe, it, expect } from "vitest";
import {
  classifyPayload,
  directionText,
  extractMeta,
  businessPayload,
  analysisOf,
} from "@/lib/event-display";

describe("classifyPayload", () => {
  it("把 _meta 单独、分析键归 analysis、其余归 business", () => {
    const data = {
      _meta: { direction: "client_to_server", msg_name: "login", semantic: ["request"] },
      playerId: 1003,
      reason: "disconnect",
      _state_changes: [{ op: "set", path: "hp" }],
      entity: "player",
      entity_id: "p1",
      entity_type: "char",
      change_count: 1,
      flow_id: "f1",
      correlation_id: "c1",
      causation_id: "e0",
      parent_id: "e-1",
      relation: "x",
    } as Record<string, unknown>;

    const { meta, business, analysis } = classifyPayload(data);
    expect(Object.keys(meta)).toEqual(["_meta"]);
    expect(Object.keys(business).sort()).toEqual(["playerId", "reason"]);
    for (const k of [
      "_state_changes",
      "entity",
      "entity_id",
      "entity_type",
      "change_count",
      "flow_id",
      "correlation_id",
      "causation_id",
      "parent_id",
      "relation",
    ]) {
      expect(analysis).toHaveProperty(k);
    }
  });

  it("空对象三份都是空", () => {
    const { meta, business, analysis } = classifyPayload({});
    expect(meta).toEqual({});
    expect(business).toEqual({});
    expect(analysis).toEqual({});
  });

  it("顶层与 _meta 重复的 msg_name/role/is_push 归 meta 而非业务数据", () => {
    const data = {
      _meta: { direction: "client_to_server", msg_name: "logout", role: "request", is_push: false },
      msg_name: "logout",
      role: "request",
      is_push: false,
      playerId: 1003,
    } as Record<string, unknown>;
    const { meta, business, analysis } = classifyPayload(data);
    expect(meta).toMatchObject({ msg_name: "logout", role: "request", is_push: false });
    expect(business).toEqual({ playerId: 1003 });
    expect(analysis).toEqual({});
  });
});

describe("directionText", () => {
  it("映射两种已知方向与未知", () => {
    expect(directionText("client_to_server")).toBe("客户端 → 服务端");
    expect(directionText("server_to_client")).toBe("服务端 → 客户端");
    expect(directionText("")).toBe("");
    expect(directionText("nope")).toBe("");
  });
});

describe("extractMeta", () => {
  it("缺失 _meta 时安全返回空默认值", () => {
    expect(extractMeta({})).toEqual({ direction: "", msgName: "", semantic: [] });
  });

  it("读取 direction / msg_name / semantic", () => {
    const m = extractMeta({
      _meta: { direction: "server_to_client", msg_name: "on_client_delta", semantic: ["response"] },
    });
    expect(m).toMatchObject({ direction: "server_to_client", msgName: "on_client_delta", semantic: ["response"] });
  });

  it("优先读取独立 meta 字段（v0.8.0）", () => {
    const m = extractMeta(
      { _meta: { direction: "client_to_server" } },
      { direction: "server_to_client", msg_name: "login", semantic: ["request"] },
    );
    expect(m).toMatchObject({ direction: "server_to_client", msgName: "login", semantic: ["request"] });
  });
});

describe("businessPayload / analysisOf（v0.8.0）", () => {
  it("有独立 meta 字段时 data 即纯业务，不再拆", () => {
    const ev = { data: { playerId: 1003 }, meta: { direction: "c2s" } };
    expect(businessPayload(ev)).toEqual({ playerId: 1003 });
  });

  it("旧数据（无独立字段）兜底拆分", () => {
    const ev = { data: { playerId: 1003, _state_changes: [] } };
    expect(businessPayload(ev)).toEqual({ playerId: 1003 });
  });

  it("analysisOf 优先独立 analysis 字段", () => {
    const ev = { data: { playerId: 1003 }, analysis: { entity: "player" } };
    expect(analysisOf(ev)).toEqual({ entity: "player" });
  });

  it("analysisOf 旧数据兜底从 data 分类", () => {
    const ev = { data: { playerId: 1003, entity_id: "p1", _state_changes: [] } };
    expect(analysisOf(ev)).toMatchObject({ entity_id: "p1", _state_changes: [] });
  });
});