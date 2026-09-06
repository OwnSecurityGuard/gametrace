import { useMemo } from "react";
import { useAccessCodes, useListProbes } from "@/hooks/use-mcp";
import type { DeviceView } from "@/types/device";
import type { AccessCode } from "@/types/access-code";
import type { ProbeInfo } from "@/types/probe";

/**
 * 派生「我的设备」列表。
 *
 * 设备 = 已接入的探针（list_probes）+ 尚未认领的启动码（等待接入）。
 * 认领之后不存在"码维度的会话"：启动码只负责把机器接进来，接入后这台机器就是一台探针，
 * 抓包由「开始抓包」下发，所以设备状态直接取探针的三维度状态（不引入 Device Service）。
 */
function deriveDevices(codes: AccessCode[], probes: ProbeInfo[]): DeviceView[] {
  const devices: DeviceView[] = probes.map((p) => {
    const capturing = p.capture_state === "running" || p.capture_state === "starting";
    return {
      kind: "probe",
      id: p.probe_id,
      probeId: p.probe_id,
      name: p.name || p.hostname || p.probe_id,
      hostname: p.hostname,
      platform: [p.os, p.arch].filter(Boolean).join("/") || undefined,
      state: p.connection_state === "online" ? (capturing ? "capturing" : "connected") : "offline",
      sessionId: p.last_session_id || undefined,
      packets: p.packets_captured ?? 0,
      lastSeen: p.last_seen_at || undefined,
      createdAt: p.created_at,
    };
  });

  // 未认领且未过期的码 = 等待接入的设备（已认领的码对应的机器已出现在 probes 里）。
  const now = Date.now();
  for (const c of codes) {
    if (c.claimed) continue;
    if (c.expires_at && new Date(c.expires_at).getTime() < now) continue;
    devices.push({
      kind: "code",
      id: c.code,
      name: c.code,
      platform: c.platform || undefined,
      state: "waiting",
      packets: 0,
      createdAt: c.created_at,
    });
  }

  // 抓包中/在线的排前面，其余按最近活动倒序；等待接入的码垫底。
  const rank: Record<DeviceView["state"], number> = {
    capturing: 0,
    connected: 1,
    offline: 2,
    stopped: 3,
    waiting: 4,
  };
  return devices.sort((a, b) => {
    const d = rank[a.state] - rank[b.state];
    if (d !== 0) return d;
    const ta = a.lastSeen ? new Date(a.lastSeen).getTime() : 0;
    const tb = b.lastSeen ? new Date(b.lastSeen).getTime() : 0;
    return tb - ta;
  });
}

export function useMyDevices(): DeviceView[] {
  const { data: probesData } = useListProbes();
  const { data: codesData } = useAccessCodes();
  const probes = probesData?.probes ?? [];
  const codes = codesData?.codes ?? [];
  return useMemo(() => deriveDevices(codes, probes), [codes, probes]);
}
