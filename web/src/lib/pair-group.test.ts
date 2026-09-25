import { describe, it, expect } from "vitest";
import type { DecodedEvent } from "@/types/event";
import { mergePartnerPool, pairGroupFilter, resolvePairGroup } from "@/lib/pair-group";

function ev(id: string, causationId?: string): DecodedEvent {
  return {
    id,
    timestamp: "2026-09-25T10:00:00Z",
    session_id: "s1",
    protocol: "lp.request",
    raw_len: 32,
    data: {},
    ...(causationId ? { causation_id: causationId } : {}),
  };
}

describe("pairGroupFilter", () => {
  it("请求按 causation_id 反查它的全部响应", () => {
    expect(pairGroupFilter(ev("req"))).toBe(`causation_id == "req"`);
  });

  it("响应取回请求本人与它的兄弟响应", () => {
    expect(pairGroupFilter(ev("resp", "req"))).toBe(`id == "req" || causation_id == "req"`);
  });
});

describe("mergePartnerPool", () => {
  it("去重、剔除自己，并优先保留页内那份", () => {
    const self = ev("req");
    const localResp = { ...ev("resp", "req"), raw_len: 10 };
    const remoteDup = { ...ev("resp", "req"), raw_len: 999 };
    const pool = mergePartnerPool(self, [self, localResp], [remoteDup, ev("resp2", "req")]);
    expect(pool.map((p) => p.id)).toEqual(["resp", "resp2"]);
    expect(pool[0]!.raw_len).toBe(10);
  });
});

describe("resolvePairGroup", () => {
  it("请求展开：左请求是自己，右响应是全部指向它的成员", () => {
    const req = ev("req");
    const { request, responses } = resolvePairGroup(req, [ev("r1", "req"), ev("r2", "req")]);
    expect(request.id).toBe("req");
    expect(responses.map((r) => r.id)).toEqual(["r1", "r2"]);
  });

  it("响应展开：请求只在服务端配对组里也能并排，且带上一问多答的兄弟", () => {
    const resp = ev("resp", "req");
    const pool = mergePartnerPool(resp, [], [ev("req"), ev("resp", "req"), ev("sibling", "req")]);
    const { request, responses } = resolvePairGroup(resp, pool);
    expect(request.id).toBe("req");
    expect(responses.map((r) => r.id)).toEqual(["resp", "sibling"]);
  });

  it("请求查不到时不出并排（退回单事件展示，不画半个空壳）", () => {
    const { request, responses } = resolvePairGroup(ev("resp", "gone"), []);
    expect(request.id).toBe("resp");
    expect(responses).toEqual([]);
  });
});
