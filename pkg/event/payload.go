package event

// Payload 包含事件的实际数据
// Value 存储实际内容
type Payload struct {
	// Value 是事件的实际数据内容
	Value Value
}

// NewPayload 创建新的 Payload
func NewPayload(value Value) Payload {
	return Payload{
		Value: value,
	}
}

// IsZero 检查 Payload 是否为零值
func (p Payload) IsZero() bool {
	return p.Value.IsNull()
}

// Get 从 Payload 的 Value 中获取指定键的值
// 如果 Value 不是对象或键不存在，返回 (ValueNull(), false)
func (p Payload) Get(key string) (Value, bool) {
	return p.Value.Get(key)
}

// MarshalMsgpack 将 Payload 编码为 MsgPack 格式
func (p Payload) MarshalMsgpack() ([]byte, error) {
	return p.Value.MarshalMsgpack()
}

// UnmarshalPayloadMsgpack 从 MsgPack 格式解码 Payload
func UnmarshalPayloadMsgpack(data []byte) (Payload, error) {
	v, err := UnmarshalValueMsgpack(data)
	if err != nil {
		return Payload{}, err
	}
	return Payload{
		Value: v,
	}, nil
}

// ToJSON 将 Payload 的 Value 编码为 JSON 字节数组
func (p Payload) ToJSON() ([]byte, error) {
	return p.Value.ToJSON()
}

// ToJSONIndent 将 Payload 的 Value 编码为格式化的 JSON 字节数组
func (p Payload) ToJSONIndent() ([]byte, error) {
	return p.Value.ToJSONIndent()
}
