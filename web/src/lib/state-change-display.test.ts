import { describe, expect, it } from "vitest";

import { beforeKind } from "./state-change-display";

describe("beforeKind", () => {
  it("before_resolved 为真：平台持有的旧值，是真实变化", () => {
    expect(beforeKind({ before_resolved: true, before: 100 })).toBe("resolved");
    // 旧值恰好为 0 / false / "" 也不能被当成缺失。
    expect(beforeKind({ before_resolved: true, before: 0 })).toBe("resolved");
    expect(beforeKind({ before_resolved: true, before: false })).toBe("resolved");
    expect(beforeKind({ before_resolved: true, before: "" })).toBe("resolved");
  });

  it("未解析且没有前值：首见（平台第一次见到该字段）", () => {
    expect(beforeKind({ before_resolved: false, before: null })).toBe("first-seen");
    expect(beforeKind({ before_resolved: false })).toBe("first-seen");
    // 字段缺失等价于未解析（老数据没有这个字段）。
    expect(beforeKind({ before: null })).toBe("first-seen");
  });

  it("未解析但插件声明了前值：参考值，不能当平台旧值用", () => {
    expect(beforeKind({ before_resolved: false, before: false })).toBe("plugin-declared");
    expect(beforeKind({ before_resolved: false, before: 0 })).toBe("plugin-declared");
    expect(beforeKind({ before_resolved: false, before: "01" })).toBe("plugin-declared");
    expect(beforeKind({ before_resolved: false, before: {} })).toBe("plugin-declared");
  });
});
