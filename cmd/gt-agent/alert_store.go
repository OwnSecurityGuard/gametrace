package main

// alert_store.go 是探针本地的「检查规则告警」存储：进程内环形缓存（最近 N 条）+
// 落盘 <spool>/alerts/<id>.json（0600），保证探针重启后详情页仍可查看。
// 同时在 <spool>/alerts.log 追加每条告警的诊断日志（收到 / 通知投递结果）。
//
// 告警由平台经 Command_Notify(alert_id + detail_json) 下发（见 control_client.go），
// 内容是 checkrule.AlertBundle（触发记录 + 各方向触发前最近 N 条解码记录）。
// 详情页由本地回环 HTTP 的 /v1/alerts/<id> 渲染（见 control_local.go）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gametrace/pkg/checkrule"
)

// alertStoreCapacity 是进程内环形缓存保留的告警条数上限（超出淘汰最旧）。
const alertStoreCapacity = 50

// alertStore 保存最近的告警 bundle，供本地详情页读取。并发安全。
type alertStore struct {
	mu    sync.RWMutex
	dir   string
	order []string // 内存条目的 alertID，新→旧
	mem   map[string]*checkrule.AlertBundle

	// addrMu 单独保护有效回环地址：control_local.Serve 绑定后写入（端口可能 +1），
	// control_client 收到告警时读取来拼详情页 URL。与告警数据锁分离避免耦合。
	addrMu sync.RWMutex
	addr   string

	// logPath 是告警诊断日志文件（<dir 同级>/alerts.log）；logMu 串行化追加写。
	logMu   sync.Mutex
	logPath string
}

// newAlertStore 构造存储并从磁盘回填内存环形缓存（重启后列表仍可见最近告警）。
func newAlertStore(dir string) *alertStore {
	s := &alertStore{dir: dir, mem: make(map[string]*checkrule.AlertBundle), logPath: alertLogPath(dir)}
	s.hydrate()
	return s
}

// alertLogPath 由告警目录推出同级日志文件路径：spool/alerts → spool/alerts.log。
func alertLogPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), "alerts.log")
}

// hydrate 从磁盘按修改时间回填最近的告警到内存缓存（best-effort）。
func (s *alertStore) hydrate() {
	if s.dir == "" {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type item struct {
		id  string
		mod time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !safeAlertID(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{id: id, mod: info.ModTime()})
	}
	// 新→旧
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	if len(items) > alertStoreCapacity {
		items = items[:alertStoreCapacity]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		b, err := s.readDisk(it.id)
		if err != nil || b == nil || b.AlertID == "" {
			continue
		}
		s.mem[b.AlertID] = b
		s.order = append(s.order, b.AlertID)
	}
}

// Put 保存一条告警：写内存环形缓存 + 落盘。alertID 为空或非法则忽略。
func (s *alertStore) Put(b checkrule.AlertBundle) {
	id := strings.TrimSpace(b.AlertID)
	if id == "" || !safeAlertID(id) {
		return
	}
	b.AlertID = id
	s.mu.Lock()
	if _, exists := s.mem[id]; !exists {
		s.order = append([]string{id}, s.order...)
	}
	cp := b
	s.mem[id] = &cp
	for len(s.order) > alertStoreCapacity {
		oldest := s.order[len(s.order)-1]
		s.order = s.order[:len(s.order)-1]
		delete(s.mem, oldest)
	}
	s.mu.Unlock()
	s.writeDisk(id, b)
}

// Get 取一条告警：内存优先，未命中回退磁盘（重启后仍可看）。
func (s *alertStore) Get(id string) (*checkrule.AlertBundle, bool) {
	id = strings.TrimSpace(id)
	if id == "" || !safeAlertID(id) {
		return nil, false
	}
	s.mu.RLock()
	if b, ok := s.mem[id]; ok {
		cp := *b
		s.mu.RUnlock()
		return &cp, true
	}
	s.mu.RUnlock()
	b, err := s.readDisk(id)
	if err != nil || b == nil {
		return nil, false
	}
	return b, true
}

// List 返回内存缓存里的告警摘要（新→旧），供 /v1/alerts JSON 列表。
func (s *alertStore) List() []alertSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]alertSummary, 0, len(s.order))
	for _, id := range s.order {
		b, ok := s.mem[id]
		if !ok {
			continue
		}
		out = append(out, alertSummary{
			AlertID:     b.AlertID,
			RuleID:      b.RuleID,
			RuleName:    b.RuleName,
			SessionID:   b.SessionID,
			Title:       b.Title,
			Message:     b.Message,
			GeneratedAt: b.GeneratedAt,
			TriggerType: b.Trigger.Type,
		})
	}
	return out
}

// alertSummary 是列表页展示用的精简字段（不含记录正文）。
type alertSummary struct {
	AlertID     string `json:"alert_id"`
	RuleID      string `json:"rule_id"`
	RuleName    string `json:"rule_name"`
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	GeneratedAt string `json:"generated_at"`
	TriggerType string `json:"trigger_type"`
}

// SetAddr 记录本地控制面的有效回环地址（Serve 绑定后调用）。
func (s *alertStore) SetAddr(addr string) {
	s.addrMu.Lock()
	s.addr = addr
	s.addrMu.Unlock()
}

// DetailURL 拼出某条告警详情页的绝对 URL；地址未知时返回空串。
func (s *alertStore) DetailURL(id string) string {
	s.addrMu.RLock()
	addr := s.addr
	s.addrMu.RUnlock()
	if addr == "" || !safeAlertID(id) {
		return ""
	}
	host := addr
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimSuffix(host, "/") + "/v1/alerts/" + id
}

// Logf 向 alerts.log 追加一行带 RFC3339 时间戳的告警诊断日志（best-effort：
// 目录不可写 / 打开失败只静默跳过，绝不影响告警存储与通知主流程）。
func (s *alertStore) Logf(format string, args ...any) {
	if s == nil || s.logPath == "" {
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

func (s *alertStore) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *alertStore) writeDisk(id string, b checkrule.AlertBundle) {
	if s.dir == "" {
		return
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	tmp := s.path(id) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path(id))
}

func (s *alertStore) readDisk(id string) (*checkrule.AlertBundle, error) {
	if s.dir == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var b checkrule.AlertBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// safeAlertID 校验 alertID 只含文件系统/URL 安全字符，杜绝路径穿越。
// alertID 由 event.NewEventID() 生成（十六进制），此处做保守白名单兜底。
func safeAlertID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
