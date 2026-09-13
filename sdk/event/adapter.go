package event

// ExtractStateChanges 从 Value 的 _state_changes 字段提取 StateChange 列表。
func ExtractStateChanges(v Value) []StateChange {
	obj, ok := v.AsObject()
	if !ok {
		return nil
	}
	raw, ok := obj["_state_changes"]
	if !ok {
		return nil
	}
	arr, ok := raw.AsArray()
	if !ok {
		return nil
	}
	var result []StateChange
	for _, item := range arr {
		io, ok := item.AsObject()
		if !ok {
			continue
		}
		sc := StateChange{}
		if v, ok := io["subject_type"]; ok {
			if s, ok := v.AsString(); ok {
				sc.SubjectType = s
			}
		}
		if v, ok := io["subject_id"]; ok {
			if s, ok := v.AsString(); ok {
				sc.SubjectID = s
			}
		}
		if v, ok := io["op"]; ok {
			if s, ok := v.AsString(); ok {
				sc.Op = s
			}
		}
		if v, ok := io["path"]; ok {
			if s, ok := v.AsString(); ok {
				sc.Path = s
			}
		}
		if v, ok := io["before"]; ok {
			sc.Before = v
		}
		if v, ok := io["after"]; ok {
			sc.After = v
		}
		if v, ok := io["version"]; ok {
			if n, ok := v.AsInt(); ok {
				sc.Version = n
			}
		}
		if v, ok := io["metadata"]; ok {
			sc.Metadata = v
		}
		result = append(result, sc)
	}
	return result
}

// 顶层分析键集合：与前端 ANALYSIS_KEYS 对齐。平台推导/投影所需的数据不放进业务 payload。
var analysisKeys = map[string]bool{
	"_state_changes": true,
	"entity":         true,
	"entity_type":    true,
	"entity_id":      true,
	"change_count":   true,
}

// 顶层元信息冗余键：direction、msg_name、role、is_push 等由系统附加的结构化字段，
// 旧插件混在 payload 顶层时拆分为 Meta。
var metaTopKeys = map[string]bool{
	"direction": true,
	"msg_name":  true,
	"role":      true,
	"is_push":   true,
}

// SplitReservedKeys 把扁平 Value 拆为 (business, meta, analysis)。
// - business: 纯业务字段（前端「业务数据」）
// - meta:     _meta 对象内容 + 顶层元信息冗余键（前端「元信息」）
// - analysis: 分析键内容（前端「分析」区域）
func SplitReservedKeys(v Value) (Value, Value, Value) {
	obj, ok := v.AsObject()
	if !ok {
		return v, Value{}, Value{}
	}
	biz := make(map[string]Value)
	meta := make(map[string]Value)
	analysis := make(map[string]Value)
	for k, val := range obj {
		switch {
		case k == "_meta":
			if mo, ok := val.AsObject(); ok {
				for mk, mv := range mo {
					meta[mk] = mv
				}
			} else {
				meta["_meta"] = val
			}
		case analysisKeys[k]:
			analysis[k] = val
		case metaTopKeys[k]:
			meta[k] = val
		default:
			biz[k] = val
		}
	}
	return ValueObject(biz), ValueObject(meta), ValueObject(analysis)
}

// MergeReservedKeys 把三段合并回扁平 Value（存储写入/旧插件兼容用）。
// 与 SplitReservedKeys 互为逆操作。
func MergeReservedKeys(business, meta, analysis Value) Value {
	obj := make(map[string]Value)
	if bo, ok := business.AsObject(); ok {
		for k, v := range bo {
			obj[k] = v
		}
	}
	if ao, ok := analysis.AsObject(); ok {
		for k, v := range ao {
			obj[k] = v
		}
	}
	if mo, ok := meta.AsObject(); ok && len(mo) > 0 {
		obj["_meta"] = ValueObject(mo)
		for k, v := range mo {
			if metaTopKeys[k] {
				obj[k] = v
			}
		}
	}
	return ValueObject(obj)
}
