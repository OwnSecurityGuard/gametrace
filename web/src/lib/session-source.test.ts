import { describe, it, expect } from "vitest";
import { captureSourceName, isProbeSource } from "@/lib/session-source";

describe("isProbeSource", () => {
  it("认得探针链路写下的全部 source（agent / probe / probe-archive）", () => {
    expect(isProbeSource("agent")).toBe(true);
    expect(isProbeSource("probe")).toBe(true);
    expect(isProbeSource("probe-archive")).toBe(true);
  });

  it("网卡抓包与代理抓包不算探针", () => {
    expect(isProbeSource("")).toBe(false);
    expect(isProbeSource("proxy")).toBe(false);
    expect(isProbeSource(undefined)).toBe(false);
  });
});

describe("captureSourceName", () => {
  it("探针会话不再被叫成「服务器网卡」", () => {
    expect(captureSourceName("probe")).toBe("抓包探针");
  });

  it("空 source 回落到网卡（历史 start_capture 默认路径）", () => {
    expect(captureSourceName("")).toBe("服务器网卡");
    expect(captureSourceName("proxy")).toBe("手机代理");
  });
});
