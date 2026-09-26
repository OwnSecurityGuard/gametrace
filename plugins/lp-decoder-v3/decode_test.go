package main

import (
	"encoding/binary"
	"testing"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
	"github.com/OwnSecurityGuard/gametrace/sdk/framing"
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"
)

func newDecoder() *decoder {
	return &decoder{reasm: framing.NewReassembler(), playerIDs: map[string]string{}}
}

func reqFor(t *testing.T, raw []byte, id string) *pb.DecodeRequest {
	t.Helper()
	return &pb.DecodeRequest{Payload: raw, LinkType: 0, InputId: id}
}

// tcpStart 定位固件帧里 TCP 头的起点（DLT_NULL 4B + IP(ihl)）。
func tcpStart(t *testing.T, raw []byte) (tcp, l4, ihl, tcpHL int) {
	t.Helper()
	ihl = int(raw[4]&0x0f) * 4
	tcp = 4 + ihl
	tcpHL = int(raw[tcp+12]>>4) * 4
	return tcp, tcp + tcpHL, ihl, tcpHL
}

// rewriteL7 保留固件的链路/IP/TCP 头，替换 L4 数据并覆写 seq（TCP 校验和不参与重组）。
func rewriteL7(t *testing.T, raw []byte, newL7 []byte, seq uint32) []byte {
	t.Helper()
	tcp, l4, ihl, tcpHL := tcpStart(t, raw)
	out := append([]byte{}, raw[:l4]...)
	out = append(out, newL7...)
	binary.BigEndian.PutUint16(out[6:], uint16(ihl+tcpHL+len(newL7)))
	binary.BigEndian.PutUint32(out[tcp+4:], seq)
	return out
}

// withTCPFlags 覆写 TCP 控制位字节（RST=0x04、SYN=0x02）。
func withTCPFlags(t *testing.T, raw []byte, flags byte) []byte {
	t.Helper()
	tcp, _, _, _ := tcpStart(t, raw)
	out := append([]byte{}, raw...)
	out[tcp+13] = flags
	return out
}

func origSeq(t *testing.T, raw []byte) uint32 {
	t.Helper()
	tcp, _, _, _ := tcpStart(t, raw)
	return binary.BigEndian.Uint32(raw[tcp+4:])
}

func l7Of(t *testing.T, raw []byte) []byte {
	t.Helper()
	_, l4, _, _ := tcpStart(t, raw)
	return raw[l4:]
}

func fixtureByTag(t *testing.T, tag string) fixture {
	t.Helper()
	for _, f := range fixtures {
		if f.tag == tag {
			return f
		}
	}
	t.Fatalf("no fixture %q", tag)
	return fixture{}
}

// TestAllFramesDecodeStandalone 逐帧独立解码（固件取自抓包不同时间点，帧间存在抓包间隙，
// 字节流连续性由下方 split/乱序/重置用例专门构造）：每帧恰好 1 条事件、
// event_type/direction 正确、通过 Draft.Validate；控制包（SYN/纯 ACK）0 事件且不是错误。
func TestAllFramesDecodeStandalone(t *testing.T) {
	for _, f := range fixtures {
		d := newDecoder()
		drafts, err := d.Decode(reqFor(t, mustFixture(t, f), f.tag))
		if err != nil {
			t.Fatalf("%s: Decode error: %v", f.tag, err)
		}
		want := 1
		if f.dir == "" {
			want = 0 // SYN / 纯 ACK：无业务字节
		}
		if len(drafts) != want {
			t.Fatalf("%s: got %d drafts, want %d", f.tag, len(drafts), want)
		}
		for _, dr := range drafts {
			if err := dr.Validate(); err != nil {
				t.Fatalf("%s: Validate: %v", f.tag, err)
			}
			if string(dr.Type) != f.name {
				t.Errorf("%s: type=%s want %s", f.tag, dr.Type, f.name)
			}
			if got := metaStr(t, dr, "direction"); got != f.dir {
				t.Errorf("%s: direction=%q want %q", f.tag, got, f.dir)
			}
			if c, ok := dr.Value.Get("cmd"); !ok || c.Kind != event.Int || c.Int != f.cmd {
				t.Errorf("%s: payload cmd=%v ok=%v want %d(int)", f.tag, c, ok, f.cmd)
			}
		}
	}
}

// TestCaptureStreamUpToFirstGap 抓包前 13 帧（实测两方向 seq 在本段内连续，含一次登录
// 失败重试与三类推送）在同一 decoder 上整段回放：每帧恰好 1 条事件。
func TestCaptureStreamUpToFirstGap(t *testing.T) {
	d := newDecoder()
	for i, f := range fixtures[:13] {
		drafts, err := d.Decode(reqFor(t, mustFixture(t, f), f.tag))
		if err != nil {
			t.Fatalf("frame %d (%s): %v", i, f.tag, err)
		}
		if len(drafts) != 1 {
			t.Fatalf("frame %d (%s): %d drafts, want 1", i, f.tag, len(drafts))
		}
	}
}

// TestStateChangesOnRealFrames 验证发现制状态投影的具体内容（声明了什么才验证什么）。
func TestStateChangesOnRealFrames(t *testing.T) {
	d := newDecoder()
	want := []struct {
		tag     string
		subject string
		n       int
	}{
		{"S2001p0", "P-1065", 3},   // player.level/exp/online，主体=线上显式 player_id
		{"S2002seq0", "5001", 1},   // item.count（绝对值），主体=item_id
		{"S2003seq0", "P-1065", 2}, // currency.gold/diamond，主体=本连接 1002/2001 建立的 player_id
	}
	idx := map[string]int{}
	for i, f := range fixtures {
		idx[f.tag] = i
	}
	// 按顺序回放到目标帧，保证货币主体归属沿连接状态自然建立。
	pos := 0
	for _, c := range want {
		for ; pos <= idx[c.tag]; pos++ {
			f := fixtures[pos]
			drafts, err := d.Decode(reqFor(t, mustFixture(t, f), f.tag))
			if err != nil {
				t.Fatalf("%s: %v", f.tag, err)
			}
			if pos != idx[c.tag] {
				continue
			}
			scs := stateChanges(t, drafts[0])
			if len(scs) != c.n {
				t.Fatalf("%s: %d state changes, want %d", c.tag, len(scs), c.n)
			}
			for _, sc := range scs {
				if s := strOf(t, sc, "subject_id"); s != c.subject {
					t.Errorf("%s: subject_id=%s want %s", c.tag, s, c.subject)
				}
				if op := strOf(t, sc, "op"); op != "set" {
					t.Errorf("%s: op=%s want set", c.tag, op)
				}
				if _, ok := sc.Get("after"); !ok {
					t.Errorf("%s: set 缺 after", c.tag)
				}
			}
		}
	}
	// 未声明状态的事件即使主体已建立也不产出 Analysis：1002 响应、1008（只有 gained 增量）。
	login := mustFixture(t, fixtureByTag(t, "S1002ok2"))
	for _, tag := range []string{"S1002ok2", "S1008gold"} {
		d2 := newDecoder()
		warm, err := d2.Decode(reqFor(t, login, "warm"))
		if err != nil {
			t.Fatal(err)
		}
		var drafts []event.Draft
		if tag == "S1002ok2" {
			drafts = warm // 登录响应本身
		} else {
			// 续接到 login 之后同一流的响应帧（固件间存在抓包间隙，seq 需接驳）
			raw := mustFixture(t, fixtureByTag(t, tag))
			next := origSeq(t, login) + uint32(len(l7Of(t, login)))
			drafts, err = d2.Decode(reqFor(t, rewriteL7(t, raw, l7Of(t, raw), next), tag))
			if err != nil {
				t.Fatalf("%s: %v", tag, err)
			}
		}
		if len(drafts) != 1 {
			t.Fatalf("%s: %d drafts", tag, len(drafts))
		}
		if !drafts[0].Analysis.IsNull() {
			t.Errorf("%s: 不应有 Analysis, got %v", tag, drafts[0].Analysis)
		}
	}
}

// TestCurrencyPushWithoutIdentity 2003 在主体未建立时仍产出事件、但不做状态投影（不猜主体）。
func TestCurrencyPushWithoutIdentity(t *testing.T) {
	d := newDecoder()
	drafts, err := d.Decode(reqFor(t, mustFixture(t, fixtureByTag(t, "S2003seq0")), "x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Type != "lp.2003" {
		t.Fatalf("drafts=%v", drafts)
	}
	if !drafts[0].Analysis.IsNull() {
		t.Errorf("主体未建立时不应有 Analysis: %v", drafts[0].Analysis)
	}
}

// TestIdentityResetOnConnectionReset 连接生命周期：2001 建立主体后同流 RST，
// 新连接上的 2003 不得沿用旧 player_id（把主体重置做成可判定行为，不靠固件巧合）。
func TestIdentityResetOnConnectionReset(t *testing.T) {
	profile := mustFixture(t, fixtureByTag(t, "S2001p0"))
	currencyL7 := l7Of(t, mustFixture(t, fixtureByTag(t, "S2003seq0")))

	d := newDecoder()
	// 1) 2001 档案推送：建立主体，产出 3 条状态。
	drafts, err := d.Decode(reqFor(t, profile, "p1"))
	if err != nil || len(drafts) != 1 || len(stateChanges(t, drafts[0])) != 3 {
		t.Fatalf("profile: drafts=%v err=%v", drafts, err)
	}
	// 2) 同一连接（借用 2001 的头部与 seq 延续）上的 2003：主体归属生效。
	seq2 := origSeq(t, profile) + uint32(len(l7Of(t, profile)))
	currency := rewriteL7(t, profile, currencyL7, seq2)
	drafts, err = d.Decode(reqFor(t, currency, "c1"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("currency on live flow: %v %v", drafts, err)
	}
	if len(stateChanges(t, drafts[0])) != 2 {
		t.Fatalf("currency should attribute to P-1065, got %v", drafts[0].Analysis)
	}
	// 3) 同流 RST（空 payload）：连接重置。
	rst := withTCPFlags(t, rewriteL7(t, profile, nil, seq2+uint32(len(currencyL7))), 0x04)
	if drafts, err := d.Decode(reqFor(t, rst, "rst")); err != nil || len(drafts) != 0 {
		t.Fatalf("rst: %v %v", drafts, err)
	}
	// 4) 重置后的新连接再收 2003：事件照常解码，但主体不再沿用 → 无状态投影。
	fresh := rewriteL7(t, profile, currencyL7, 1000)
	drafts, err = d.Decode(reqFor(t, fresh, "c2"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("currency after reset: %v %v", drafts, err)
	}
	if !drafts[0].Analysis.IsNull() {
		t.Errorf("旧主体不应跨连接存活: %v", drafts[0].Analysis)
	}
}

// TestFrameSplitAcrossSegments 一帧被 TCP 切成两段：两段合计产出恰好 1 条事件。
func TestFrameSplitAcrossSegments(t *testing.T) {
	raw := mustFixture(t, fixtureByTag(t, "S2001p0"))
	l7 := l7Of(t, raw)
	seq0 := origSeq(t, raw)
	mid := len(l7) / 2

	d := newDecoder()
	got := 0
	for _, seg := range [][]byte{
		rewriteL7(t, raw, l7[:mid], seq0),
		rewriteL7(t, raw, l7[mid:], seq0+uint32(mid)),
	} {
		drafts, err := d.Decode(reqFor(t, seg, "s"))
		if err != nil {
			t.Fatal(err)
		}
		got += len(drafts)
	}
	if got != 1 {
		t.Fatalf("split frame produced %d drafts, want 1", got)
	}
}

// TestOutOfOrderAndRetransmit 先经 SYN 建立序列号基线，再乱序投递：后段先到不产出、
// 前段补齐恰好 1 条、重传不重复。
func TestOutOfOrderAndRetransmit(t *testing.T) {
	raw := mustFixture(t, fixtureByTag(t, "S2001lv2"))
	l7 := l7Of(t, raw)
	seq0 := origSeq(t, raw)
	mid := len(l7) / 2
	syn := withTCPFlags(t, rewriteL7(t, raw, nil, seq0-1), 0x02) // SYN 消耗 1 个序号，数据基线 = seq0
	p1 := rewriteL7(t, raw, l7[:mid], seq0)
	p2 := rewriteL7(t, raw, l7[mid:], seq0+uint32(mid))

	d := newDecoder()
	count := func(seg []byte) int {
		n, err := d.Decode(reqFor(t, seg, "o"))
		if err != nil {
			t.Fatal(err)
		}
		return len(n)
	}
	if got := count(syn); got != 0 {
		t.Fatalf("syn produced %d drafts, want 0", got)
	}
	if got := count(p2); got != 0 { // 前缀缺失，不能误产出
		t.Fatalf("tail-first produced %d drafts, want 0", got)
	}
	if got := count(p1); got != 1 {
		t.Fatalf("after head produced %d drafts, want 1", got)
	}
	if got := count(p1); got != 0 { // 重传不重复
		t.Fatalf("retransmit produced %d drafts, want 0", got)
	}
}

// TestDesyncIsObservableAndFlowRecovers 垃圾字节判失步：错误可观测；
// 该方向缓冲被丢弃后，同连接下一帧仍能正常解码。
func TestDesyncIsObservableAndFlowRecovers(t *testing.T) {
	raw := mustFixture(t, fixtureByTag(t, "S2003seq0"))
	d := newDecoder()

	if _, err := d.Decode(reqFor(t, rewriteL7(t, raw, []byte("not-a-lp-frame"), origSeq(t, raw)), "junk")); err == nil {
		t.Fatal("expected desync error, got nil")
	}
	drafts, err := d.Decode(reqFor(t, raw, "after"))
	if err != nil {
		t.Fatalf("recovery Decode: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("recovered drafts=%d want 1", len(drafts))
	}
}

// ---- helpers ----

func metaStr(t *testing.T, dr event.Draft, key string) string {
	t.Helper()
	if dr.Meta.IsNull() {
		return ""
	}
	v, _ := dr.Meta.Get(key)
	s, _ := v.AsString()
	return s
}

func stateChanges(t *testing.T, dr event.Draft) []event.Value {
	t.Helper()
	if dr.Analysis.IsNull() {
		return nil
	}
	v, ok := dr.Analysis.Get("_state_changes")
	if !ok {
		t.Fatalf("no _state_changes in %v", dr.Analysis)
	}
	arr, _ := v.AsArray()
	return arr
}

func strOf(t *testing.T, v event.Value, key string) string {
	t.Helper()
	k, ok := v.Get(key)
	if !ok {
		t.Fatalf("missing %q", key)
	}
	s, _ := k.AsString()
	return s
}
