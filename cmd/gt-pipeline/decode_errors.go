package main

import (
	"context"
	"log/slog"
	"time"

	"gametrace/pkg/decode"
	"gametrace/pkg/internalipc/capturecontrol"
	"gametrace/pkg/store"
)

// 解码失败从来不只是"一个数字"：用户看到「解码失败 12,345 次」时，真正的问题是
// "解不开的是什么、为什么"。这里负责把采集到的失败分组落库，使会话**停止之后**
// 仍能回答这个问题 —— 只在内存里活着的原因等于没有原因。
//
// 采集本身在 pkg/decode（ErrorCollector），落库在 pkg/store，这里只做接线。
// 实时抓包（capture_task.go）与离线重解码（decode_raw.go）共用这里的两个函数，
// 避免两条路径各写一份转换。

// decodeErrorPersistTimeout 限制落库耗时：终止流程不能被写失败拖住。
const decodeErrorPersistTimeout = 5 * time.Second

// decodeErrorPersistInterval 是运行中失败分组的落库间隔。
//
// 只在会话停止时落库是不够的：用户看到「失败 5000 次」时通常还在抓，
// 那一刻问的就是"为什么"。节流是为了不把每轮 flush 都变成一次写库。
const decodeErrorPersistInterval = 5 * time.Second

// toDecodeErrorRows 把聚合分组转成落库行。
func toDecodeErrorRows(sessionID string, col *decode.ErrorCollector) []store.DecodeErrorRow {
	groups := col.Groups()
	if len(groups) == 0 {
		return nil
	}
	rows := make([]store.DecodeErrorRow, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, store.DecodeErrorRow{
			Fingerprint: g.Fingerprint,
			Kind:        g.Kind,
			Template:    g.Template,
			Sample:      g.Sample,
			SampleRawID: g.SampleRawID,
			SampleSrc:   g.SampleSrc,
			SampleDst:   g.SampleDst,
			Count:       g.Count,
			FirstSeen:   g.FirstSeen,
			LastSeen:    g.LastSeen,
		})
	}
	return rows
}

// persistDecodeErrors 把失败分组整体写入会话库。
//
// 写入用「整体替换」语义，所以即使一次都没失败过也要写一次（传 nil 即清空）——
// 否则重解码前一轮留下的旧分组会被误当成这一轮的结果。
//
// 写失败不影响终止流程（会话该停还是要停），但必须留痕：静默丢数据是这条链路
// 反复出现过的问题，不要在新增的这一步上再犯。
func persistDecodeErrors(ctx context.Context, st store.DecodeErrorWriter, sessionID string,
	col *decode.ErrorCollector, logger *slog.Logger) {
	if st == nil {
		return
	}
	rows := toDecodeErrorRows(sessionID, col)
	wctx, cancel := context.WithTimeout(ctx, decodeErrorPersistTimeout)
	defer cancel()
	if err := st.ReplaceDecodeErrorGroups(wctx, sessionID, rows); err != nil {
		logger.Error("write decode error groups failed",
			"error", err, "session_id", sessionID, "groups", len(rows), "total", col.Total())
		return
	}
	if len(rows) > 0 {
		logger.Info("decode error groups persisted",
			"session_id", sessionID, "groups", len(rows), "total", col.Total())
	}
}

// errorSamplesFromGroups 把失败分组摊平成样本列表（每组一条，次数多的在前）。
//
// 插件测试用它替代「顺序取前 N 条」：去重后样本能覆盖不同种类，而不是被最先
// 出现的那一种错误占满 —— 那恰好是用户最需要区分同类错误的时候。
func errorSamplesFromGroups(col *decode.ErrorCollector, limit int) []capturecontrol.TestErrorLite {
	if limit <= 0 {
		return nil
	}
	groups := col.Groups()
	if len(groups) == 0 {
		return nil
	}
	if len(groups) > limit {
		groups = groups[:limit]
	}
	out := make([]capturecontrol.TestErrorLite, 0, len(groups))
	for _, g := range groups {
		out = append(out, capturecontrol.TestErrorLite{
			RawPacketID: g.SampleRawID,
			Src:         g.SampleSrc,
			Dst:         g.SampleDst,
			Error:       g.Sample,
		})
	}
	return out
}
