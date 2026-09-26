import { describe, it, expect } from "vitest";
import { parseRoute } from "@/lib/router";
import {
  WORKSPACE_HREF,
  projectHref,
  projectSpaceHref,
  sessionHref,
  routeKey,
  visibleSessionViews,
} from "@/lib/routes";

describe("parseRoute", () => {
  it("空 hash 与未知路径回落工作台空间", () => {
    for (const hash of ["", "#", "#/", "#/workspace", "#/nonsense", "#/nonsense/x"]) {
      expect(parseRoute(hash).space).toBe("workspace");
      expect(parseRoute(hash).sessionId).toBeNull();
      expect(parseRoute(hash).projectId).toBeNull();
    }
  });

  it("项目空间：项目 id 与配置页签由路径段派生", () => {
    const p = parseRoute("#/project/p-gateway");
    expect(p).toMatchObject({ space: "project", projectId: "p-gateway", projectSection: "sessions" });

    const cfg = parseRoute("#/project/p-gateway/config/plugins");
    expect(cfg).toMatchObject({ space: "project", projectId: "p-gateway", projectSection: "config", configTab: "plugins" });

    // 未知页签回落默认，而不是渲染空白
    expect(parseRoute("#/project/p-gateway/config/nope").configTab).toBe("members");
    expect(parseRoute("#/project/p-gateway/config").configTab).toBe("members");
  });

  it("会话空间：视图由末段派生，未知视图回落概览", () => {
    const s = parseRoute("#/session/s-2c91de/events");
    expect(s).toMatchObject({ space: "session", sessionId: "s-2c91de", view: "events" });

    expect(parseRoute("#/session/s-2c91de").view).toBe("overview");
    expect(parseRoute("#/session/s-2c91de/nope").view).toBe("overview");
    // 「命中提醒」是常开视图，不需要任何构建期开关
    expect(parseRoute("#/session/s-2c91de/alerts").view).toBe("alerts");
    // 「原始包」是解码调试视图，raw-debug 关闭时不可达
    const rawAvailable = visibleSessionViews().some((v) => v.id === "raw");
    expect(parseRoute("#/session/s-2c91de/raw").view).toBe(rawAvailable ? "raw" : "overview");
  });

  it("未归属桶与项目同构：projectId 为空即「未归属」", () => {
    const u = parseRoute("#/unassigned");
    expect(u).toMatchObject({ space: "project", projectId: null, projectSection: "sessions" });
    // 未知路径不该掉进这个桶——它回落工作台，项目上下文仍然是"没有项目"。
    expect(parseRoute("#/nonsense").projectId).toBeNull();
  });

  it("会话空间不携带项目 id：归属只有后端一个真相来源", () => {
    expect(parseRoute("#/session/s-2c91de/events").projectId).toBeNull();
  });

  it("忽略查询串，编码过的 id 原样还原", () => {
    expect(parseRoute("#/session/s-1/events?demo=first-run").sessionId).toBe("s-1");
    expect(parseRoute(projectHref("p a/b")).projectId).toBe("p a/b");
  });
});

describe("href 生成", () => {
  it("与 parseRoute 往返一致", () => {
    expect(parseRoute(WORKSPACE_HREF).space).toBe("workspace");
    for (const view of ["overview", "connections", "events", "states"] as const) {
      expect(parseRoute(sessionHref("s-1", view))).toMatchObject({ space: "session", sessionId: "s-1", view });
    }
    expect(parseRoute(projectHref("p-1", "config", "rules"))).toMatchObject({
      space: "project",
      projectId: "p-1",
      projectSection: "config",
      configTab: "rules",
    });
  });

  it("projectSpaceHref：有归属进项目，无归属进未归属桶", () => {
    expect(parseRoute(projectSpaceHref("p-1")).projectId).toBe("p-1");
    for (const id of [null, undefined, ""]) {
      expect(parseRoute(projectSpaceHref(id))).toMatchObject({
        space: "project",
        projectId: null,
      });
    }
  });
});

describe("routeKey", () => {
  it("换视图不换会话、换会话必换", () => {
    expect(routeKey(parseRoute("#/session/s-1/events"))).toBe(routeKey(parseRoute("#/session/s-1/states")));
    expect(routeKey(parseRoute("#/session/s-1/events"))).not.toBe(routeKey(parseRoute("#/session/s-2/events")));
  });
});
