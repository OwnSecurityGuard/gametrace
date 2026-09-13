package rule

import (
	"encoding/json"

	"github.com/tidwall/gjson"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// PairHit 是一条 pair 规则在单个事件上的命中。
// 平台侧配对语义：两个事件的 PairHit 满足 MatchPair 时构成 Request ↔ Response
// 式配对（同规则、键相等、各占一个不同 side）。
// Key 来自命中 side 自己声明的 key（GJSON path），两侧字段结构可以不同。
type PairHit struct {
	RuleID string
	Key    string // 配对键的字符串化取值（取自命中的 side 的 key path）
	Side   int    // 命中的 sides 下标（pair 恰好 2 个 side）
}

// MatchPair 报告两个 PairHit 是否可配对：同规则、键相等、
// 且各匹配一个不同 side。
func MatchPair(a, b PairHit) bool {
	if a.RuleID == "" || a.RuleID != b.RuleID {
		return false
	}
	if a.Key != b.Key {
		return false
	}
	// 双方都命中 side 时必须不同（A 是请求方，B 是响应方）。
	if a.Side >= 0 || b.Side >= 0 {
		return a.Side != b.Side
	}
	return true
}

// NameHit 是一条 name 规则在单个事件上的命中：从 payload 提取到的消息名称。
// 宿主把首个命中写入 meta.msg_name（规则优先于解码器硬编码）。
type NameHit struct {
	RuleID string
	Value  string // 消息名称的字符串化取值
}

// Child 是一条 extract 规则产出的子事件。
// 子事件不是三条新网络包：宿主必须保留其与父事件的来源关系（§7）。
type Child struct {
	RuleID    string
	EventType string // child.event_type
	SchemaID  string // child.schema_id
	Index     int    // 在 Source 数组中的下标；Source 为对象时为 0
	Value     event.Value
}

// Result 是一组语义规则在单个事件上的评估产出。
type Result struct {
	Semantics []Semantic // annotate 命中的语义标签（去重保序）
	Pairs     []PairHit  // pair 命中（键 + 角色），实际配对由平台执行
	Children  []Child    // extract 产出的子事件
	Names     []NameHit  // name 命中的消息名提取（规则声明的优先顺序）
}

// Evaluate 对一个事件的 payload 执行全部语义规则。
//
// Event JSON → GJSON 取值 → Predicate 判断 → Effect 产生语义（§3 / §13）。
// payload 经 Value.MarshalJSON 转 JSON 后用 GJSON path 取值；extract 的子事件
// 载荷从对应 JSON 元素还原为 event.Value（整数在范围内保持 Int 精度）。
//
// 求值顺序分两遍：name 效果先执行，把提取到的消息名注入求值视图的
// _meta.msg_name，再评估其余效果——这样 annotate/pair 规则可以按
// _meta.msg_name 做谓词判定（与解码器硬编码 msg_name 时的行为一致）。
//
// 非法规则（未先 Validate）被跳过，不 panic；decode 的 panic 边界由 SDK wrapper 兜底，
// 这里同样遵守"网络字节不使进程崩溃"的契约。
func Evaluate(rules []Rule, v event.Value) (Result, error) {
	var res Result

	data, err := v.MarshalJSON()
	if err != nil {
		return res, err
	}
	doc := gjson.ParseBytes(data)

	// 第一遍：name 效果提取消息名，并把首个命中注入 _meta.msg_name。
	for _, r := range rules {
		if r.Effect.Type != EffectName {
			continue
		}
		if r.When == nil || !r.When.Evaluate(doc) {
			continue
		}
		val, ok := evalName(r, doc)
		if !ok {
			continue
		}
		res.Names = append(res.Names, NameHit{RuleID: r.ID, Value: val})
		if len(res.Names) == 1 {
			if patched, ok := patchMsgName(doc, val); ok {
				doc = patched
			}
		}
	}

	// 第二遍：其余效果（pair / extract / annotate）。
	for _, r := range rules {
		if r.Effect.Type == EffectName {
			continue
		}
		if r.When == nil || !r.When.Evaluate(doc) {
			continue
		}
		switch r.Effect.Type {
		case EffectPair:
			if hit, ok := evalPair(r, doc); ok {
				res.Pairs = append(res.Pairs, hit)
			}
		case EffectExtract:
			res.Children = append(res.Children, evalExtract(r, doc)...)
		case EffectAnnotate:
			if !containsSemantic(res.Semantics, r.Effect.Semantic) {
				res.Semantics = append(res.Semantics, r.Effect.Semantic)
			}
		}
	}
	return res, nil
}

func evalPair(r Rule, doc gjson.Result) (PairHit, bool) {
	// 每侧自带 key：先按该侧的角色判定（Predicate）确认事件属于哪一侧，
	// 再用该侧的 key（GJSON path）提取配对键 —— 两侧字段结构不同也能配对。
	for i := range r.Effect.Sides {
		s := &r.Effect.Sides[i]
		if !s.Evaluate(doc) {
			continue
		}
		keyRes := doc.Get(s.Key)
		if !keyRes.Exists() {
			return PairHit{}, false
		}
		return PairHit{RuleID: r.ID, Key: keyStr(keyRes), Side: i}, true
	}
	// 事件满足 when 但不扮演任何一侧角色：不参与配对。
	return PairHit{}, false
}

// evalName 提取 name 规则的消息名：Key 取值必须存在且字符串化非空。
func evalName(r Rule, doc gjson.Result) (string, bool) {
	keyRes := doc.Get(r.Effect.Key)
	if !keyRes.Exists() {
		return "", false
	}
	val := keyStr(keyRes)
	if val == "" {
		return "", false
	}
	return val, true
}

// patchMsgName 把提取到的消息名写入求值视图的 _meta.msg_name（不存在则创建 _meta），
// 供同事件内 annotate/pair 等后续规则的 _meta.msg_name 谓词使用。
// _meta 已存在但非对象时不覆盖（防御性跳过）。
func patchMsgName(doc gjson.Result, name string) (gjson.Result, bool) {
	root, ok := doc.Value().(map[string]any)
	if !ok {
		return doc, false
	}
	metaRaw, has := root["_meta"]
	if has && metaRaw != nil {
		meta, ok := metaRaw.(map[string]any)
		if !ok {
			return doc, false
		}
		meta["msg_name"] = name
	} else {
		root["_meta"] = map[string]any{"msg_name": name}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return doc, false
	}
	return gjson.ParseBytes(raw), true
}

func evalExtract(r Rule, doc gjson.Result) []Child {
	src := doc.Get(r.Effect.Source)
	if !src.Exists() || r.Effect.Child == nil {
		return nil
	}
	var out []Child
	emit := func(elem gjson.Result, idx int) {
		v, err := event.ValueFromJSON([]byte(elem.Raw))
		if err != nil {
			return // 元素不是合法 JSON（不可能，来自已解析文档）；防御性跳过
		}
		out = append(out, Child{
			RuleID:    r.ID,
			EventType: r.Effect.Child.EventType,
			SchemaID:  r.Effect.Child.SchemaID,
			Index:     idx,
			Value:     v,
		})
	}
	switch {
	case src.IsArray():
		for i, e := range src.Array() {
			emit(e, i)
		}
	case src.Type == gjson.JSON: // 对象 → 单个子事件
		emit(src, 0)
	default:
		// 标量/null：无可提取结构，静默跳过（SyncDbData: null 是合法形态）
	}
	return out
}

// keyStr 把配对键取值字符串化：数字/字符串/布尔按 JSON 文本形态。
func keyStr(res gjson.Result) string { return res.String() }

func containsSemantic(list []Semantic, s Semantic) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
