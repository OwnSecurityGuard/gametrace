package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceAndQueryDecodeErrorGroups(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	base := time.Unix(1700000000, 0)
	if err := s.ReplaceDecodeErrorGroups(ctx, "sess-1", []DecodeErrorRow{
		{Fingerprint: "fp-a", Kind: "plugin", Template: "unknown type <n>", Sample: "unknown type 7",
			SampleRawID: "raw-1", SampleSrc: "10.0.0.1:5", SampleDst: "10.0.0.2:9",
			Count: 3, FirstSeen: base, LastSeen: base.Add(time.Second)},
		{Fingerprint: "fp-b", Kind: "transport", Template: "stream closed", Sample: "stream closed",
			SampleRawID: "raw-2", Count: 40, FirstSeen: base, LastSeen: base.Add(2 * time.Second)},
	}); err != nil {
		t.Fatalf("ReplaceDecodeErrorGroups: %v", err)
	}

	got, err := s.QueryDecodeErrorGroups(ctx, "sess-1")
	if err != nil {
		t.Fatalf("QueryDecodeErrorGroups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("分组数 = %d, want 2", len(got))
	}
	// 按次数降序：40 次的传输错误应排在 3 次的插件错误前面。
	if got[0].Fingerprint != "fp-b" || got[0].Count != 40 {
		t.Errorf("首行 = (%s, %d), want (fp-b, 40)：应按次数降序", got[0].Fingerprint, got[0].Count)
	}
	if got[1].Sample != "unknown type 7" || got[1].SampleRawID != "raw-1" {
		t.Errorf("样本未往返：%+v", got[1])
	}
	if !got[1].FirstSeen.Equal(base) || !got[1].LastSeen.Equal(base.Add(time.Second)) {
		t.Errorf("时间未往返：first=%v last=%v", got[1].FirstSeen, got[1].LastSeen)
	}
	if got[0].SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1（查询应回填）", got[0].SessionID)
	}
}

// 快照语义：第二次写入是替换而不是累加，否则同一次失败会被算两遍。
func TestReplaceDecodeErrorGroupsIsSnapshotNotAccumulate(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	first := []DecodeErrorRow{{Fingerprint: "fp-a", Kind: "plugin", Template: "boom", Count: 5}}
	if err := s.ReplaceDecodeErrorGroups(ctx, "s", first); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDecodeErrorGroups(ctx, "s", first); err != nil {
		t.Fatal(err)
	}

	got, err := s.QueryDecodeErrorGroups(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 5 {
		t.Fatalf("写入两次同一快照后 = %+v, want 单行 Count=5（不应累加成 10）", got)
	}
}

func TestReplaceDecodeErrorGroupsEmptyClears(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.ReplaceDecodeErrorGroups(ctx, "s", []DecodeErrorRow{
		{Fingerprint: "fp-a", Kind: "plugin", Template: "boom", Count: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// 离线重解码前要先清掉上一轮的结果。
	if err := s.ReplaceDecodeErrorGroups(ctx, "s", nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.QueryDecodeErrorGroups(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("清空后仍剩 %d 行", len(got))
	}
}

// 会话之间互不干扰：A 的快照不能影响 B。
func TestReplaceDecodeErrorGroupsIsolatesSessions(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	row := DecodeErrorRow{Fingerprint: "fp-a", Kind: "plugin", Template: "boom", Count: 7}
	if err := s.ReplaceDecodeErrorGroups(ctx, "a", []DecodeErrorRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDecodeErrorGroups(ctx, "b", nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.QueryDecodeErrorGroups(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Count != 7 {
		t.Fatalf("会话 a 的分组被别的会话影响了：%+v", got)
	}
}

// 零值时间必须是零值往返，不能变成 1970 年或天文数字（溢出）。
func TestDecodeErrorGroupZeroTimeRoundTrip(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	if err := s.ReplaceDecodeErrorGroups(ctx, "s", []DecodeErrorRow{
		{Fingerprint: "fp", Kind: "plugin", Template: "boom", Count: 1},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryDecodeErrorGroups(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].FirstSeen.IsZero() {
		t.Errorf("FirstSeen = %v, want 零值", got[0].FirstSeen)
	}
}
