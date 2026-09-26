import { useMemo } from "react";
import { useListProbes } from "@/hooks/use-mcp";
import type { DeviceView } from "@/types/device";
import type { ProbeInfo } from "@/types/probe";

/**
 * 派生「我的设备」列表。
 *
 * 设备 = 已接入的探针（list_probes）。接入动作只有一种：下载 gt-agent 并在目标机运行，
 * 探针回连后出现在这里；抓包由「开始抓包」下发，设备状态直接取探针的三维度状态。
 */
function deriveDevices(probes: ProbeInfo[]): DeviceView[] {
  return probes
    .map((p): DeviceView => {
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
    })
    .sort((a, b) => {
      const rank: Record<DeviceView["state"], number> = {
        capturing: 0,
        connected: 1,
        offline: 2,
        stopped: 3,
      };
      const d = rank[a.state] - rank[b.state];
      if (d !== 0) return d;
      const ta = a.lastSeen ? new Date(a.lastSeen).getTime() : 0;
      const tb = b.lastSeen ? new Date(b.lastSeen).getTime() : 0;
      return tb - ta;
    });
}

export function useMyDevices(): DeviceView[] {
  const { data: probesData } = useListProbes();
  const probes = probesData?.probes ?? [];
  return useMemo(() => deriveDevices(probes), [probes]);
}