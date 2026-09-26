package decode

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"gametrace/pkg/event"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

// TestConvertResultsAnalysisFailureObservableKeepsEvent 覆盖 analysis_msgpack 反解失败的处理：
// 事件本体（payload/meta）必须保留，但失败必须进 ErrorCollector——
// 「状态投影静默消失」曾是全链路最难发现的失败形态。
func TestConvertResultsAnalysisFailureObservableKeepsEvent(t *testing.T) {
	d := &Dispatcher{
		sessionID:    "s1",
		errs:         NewErrorCollector(),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		causationIdx: newCausationIndex(16),
	}
	payload, err := event.ValueObject(map[string]event.Value{"hp": event.ValueInt(1)}).MarshalMsgpack()
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.DecodeRequest{InputId: "in1", PacketId: "p1", TimestampNs: 1}
	results := []*pb.DecodeResponseV2{
		{InputId: "in1", EventType: "game.hit", PayloadMsgpack: payload, AnalysisMsgpack: []byte{0xc1, 0xff, 0x00}},
	}

	events := d.convertResultsToEvents(req, results, "", "src", "")
	if len(events) != 1 {
		t.Fatalf("analysis 解不开不该带走整条事件, got %d events", len(events))
	}
	if !events[0].Analysis.IsNull() && len(events[0].Analysis.Object) != 0 {
		t.Errorf("反解失败后 Analysis 应无内容, got %v", events[0].Analysis)
	}
	if len(events[0].ExtractStateChanges()) != 0 {
		t.Errorf("反解失败后不应有任何状态变更投影, got %v", events[0].ExtractStateChanges())
	}
	if d.errs.Total() != 1 {
		t.Fatalf("analysis 解码失败未计入 ErrorCollector, total = %d", d.errs.Total())
	}
	groups := d.errs.Groups()
	if len(groups) != 1 || groups[0].Kind != ErrKindPlugin || !strings.Contains(groups[0].Sample, "unmarshal msgpack analysis") {
		t.Errorf("错误组 = %+v, want plugin 类且 sample 含 unmarshal msgpack analysis", groups)
	}
}
