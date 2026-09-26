import { describe, expect, it } from "vitest";
import {
  describeErrorKind,
  errorKindTone,
  summarizeDecodeErrors,
} from "@/lib/decode-error-display";
import type { DecodeErrorGroup } from "@/types/decode-error";

function group(over: Partial<DecodeErrorGroup> = {}): DecodeErrorGroup {
  return { kind: "plugin", template: "boom", count: 1, ...over };
}

describe("describeErrorKind", () => {
  it("区分插件报错、链路失败与插件未接入", () => {
    expect(describeErrorKind("plugin")).toBe("插件无法解析");
    expect(describeErrorKind("transport")).toBe("解码链路中断");
    expect(describeErrorKind("binding")).toBe("解析插件未接入");
  });

  it("未知来源不炸也不撒谎", () => {
    expect(describeErrorKind("something-new")).toBe("其他失败");
  });
});

describe("errorKindTone", () => {
  it("插件错误是待排查（warn），链路中断与插件未接入是故障（error）", () => {
    expect(errorKindTone("plugin")).toBe("warn");
    expect(errorKindTone("transport")).toBe("error");
    // binding 意味着整场会话一个事件都不会有，比单包链路错误更该显眼。
    expect(errorKindTone("binding")).toBe("error");
    expect(errorKindTone("?")).toBe("muted");
  });
});

describe("summarizeDecodeErrors", () => {
  it("没有失败时不提原因", () => {
    expect(summarizeDecodeErrors([], 0)).toBe("没有解码失败");
  });

  it("有失败但没有分组时说明是未记录，不谎称没有失败", () => {
    expect(summarizeDecodeErrors([], 42)).toContain("未记录");
  });

  it("只有一类时直接点名", () => {
    expect(summarizeDecodeErrors([group({ count: 100 })], 100)).toBe(
      "全部是同一类错误：插件无法解析",
    );
  });

  it("只有 1 次的插件未接入也要被点名 —— 它解释的是整场 0 事件", () => {
    expect(summarizeDecodeErrors([group({ kind: "binding", count: 1 })], 1)).toBe(
      "全部是同一类错误：解析插件未接入",
    );
  });

  it("主因占比高时点名并给出占比", () => {
    const groups = [
      group({ count: 90, kind: "transport" }),
      group({ count: 10, kind: "plugin" }),
    ];
    expect(summarizeDecodeErrors(groups, 100)).toBe("主要是解码链路中断（占 90%）");
  });

  it("错误分散时不硬点名主因", () => {
    const groups = [
      group({ count: 34 }),
      group({ count: 33 }),
      group({ count: 33 }),
    ];
    const text = summarizeDecodeErrors(groups, 100);
    expect(text).toBe("错误分散在 3 类里，最常见的是插件无法解析");
    expect(text).not.toContain("主要是");
  });
});
