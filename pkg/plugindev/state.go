package plugindev

import (
	"sync"
)

// Tracker 持有本进程内最近一次的 verify 结果，用于
// plugin.explain 在调用方未内联传入 verify 时的归因兜底。
//
// 它刻意不做任何持久化：verdict 的权威来源是 verify_plugin 的返回值本身；
// 「插件实例已验证」这条跨进程证据存在平台数据库（store.PluginValidation），
// 由 gt-pipeline 写入、gt-mcp 读取，与进程内 Tracker 无关。
type Tracker struct {
	mu         sync.Mutex
	lastVerify map[string]*VerifyResult
}

// defaultTracker is the process-wide attribution state.
var defaultTracker = NewTracker()

// NewTracker constructs an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		lastVerify: make(map[string]*VerifyResult),
	}
}

// RecordVerify stores the most recent verify result for a plugin so a later
// explain can attribute it without the caller re-supplying the corpus.
func (t *Tracker) RecordVerify(name string, r *VerifyResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastVerify[name] = r
}

// LastVerify returns the most recent verify result for a plugin, or nil.
func (t *Tracker) LastVerify(name string) *VerifyResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastVerify[name]
}

// RecordVerify is the package-level entry point for storing a plugin's latest
// verify result on the process-wide tracker.
func RecordVerify(name string, r *VerifyResult) { defaultTracker.RecordVerify(name, r) }