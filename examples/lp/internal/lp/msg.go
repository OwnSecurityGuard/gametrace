package lp

import "encoding/json"

// lp 模板协议的消息表。号段划分与 examples/lp-decoder 的语义规则保持一致：
//
//	1000~1999  请求/响应成对：请求取奇数，响应号 = 请求号 + 1（回显同一 seq 配对）
//	2000~2999  服务端主动推送（notification，seq 恒为 0）
//	9000~9999  协议级错误回包（信封本身不可用，映射不到业务响应号）
const (
	CmdLoginRequest    = 1001
	CmdLoginResponse   = 1002
	CmdGetBagRequest   = 1003
	CmdGetBagResponse  = 1004
	CmdUseItemRequest  = 1005
	CmdUseItemResponse = 1006
	CmdGatherRequest   = 1007
	CmdGatherResponse  = 1008

	CmdPlayerInfoNotify = 2001 // 登录成功后推送玩家档案
	CmdItemCountNotify  = 2002 // 道具数量变化
	CmdResourceNotify   = 2003 // 资源（金币/钻石）数量变化

	CmdBadRequest = 9001 // 信封级错误：无法解析 / 未知 cmd
)

// 错误码：error_code != 0 即命中解码器的 error 语义，error_msg 是服务端给的可读提示。
const (
	ErrNone            = 0
	ErrBadEnvelope     = 1 // payload 不是合法 JSON 信封
	ErrUnknownCmd      = 2 // cmd 未定义（含 cmd 字段缺失时的 0）
	ErrNotLoggedIn     = 3 // 未登录就请求业务消息
	ErrAlreadyLoggedIn = 4 // 同一连接重复登录
	ErrInvalidParam    = 5 // 参数缺失、非正数或超出上限
	ErrItemNotFound    = 6 // 背包里没有该道具
	ErrItemNotEnough   = 7 // 道具数量不足
	ErrResourceLow     = 8 // 资源不足以支付消耗
)

// cmdNames 只用于两侧日志展示，与解码插件的 cmd→名称映射表保持一致。
var cmdNames = map[int]string{
	CmdLoginRequest:     "LoginRequest",
	CmdLoginResponse:    "LoginResponse",
	CmdGetBagRequest:    "GetBagRequest",
	CmdGetBagResponse:   "GetBagResponse",
	CmdUseItemRequest:   "UseItemRequest",
	CmdUseItemResponse:  "UseItemResponse",
	CmdGatherRequest:    "GatherRequest",
	CmdGatherResponse:   "GatherResponse",
	CmdPlayerInfoNotify: "PlayerInfoNotify",
	CmdItemCountNotify:  "ItemCountNotify",
	CmdResourceNotify:   "ResourceNotify",
	CmdBadRequest:       "BadRequest",
}

// CmdName 返回 cmd 的符号名，未定义为 "unknown"。
func CmdName(cmd int) string {
	if name, ok := cmdNames[cmd]; ok {
		return name
	}
	return "unknown"
}

// IsRequestCmd 判别客户端请求号：请求/响应成对且请求为奇数，
// 所以解码器无需额外字段就能推出方向（模板协议特有约定）。
func IsRequestCmd(cmd int) bool { return cmd >= 1000 && cmd < 2000 && cmd%2 == 1 }

// IsPushCmd 判别服务端推送号段。
func IsPushCmd(cmd int) bool { return cmd >= 2000 && cmd < 3000 }

// ResponseCmd 给出一条请求对应的响应号（校验失败的响应也用它，错误信息放在 error_code）。
func ResponseCmd(cmd int) int { return cmd + 1 }

// Envelope 是 lp 帧 payload 携带的 JSON 信封：协议签名 + 业务消息体。
// 解码器按 cmd / seq / error_code 提取语义，data 原样保留在 body 文本里。
type Envelope struct {
	Cmd       int             `json:"cmd"`
	Seq       int             `json:"seq"`
	ErrorCode int             `json:"error_code,omitempty"`
	ErrorMsg  string          `json:"error_msg,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// MarshalData 序列化业务消息体；nil 表示无消息体。
func MarshalData(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	return raw
}

// UnmarshalData 解析信封里的消息体；缺省消息体解出零值。
func UnmarshalData(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

// 业务消息体：字段命名刻意保持通用，不对应任何真实游戏协议。

type LoginRequestData struct {
	Account  string `json:"account"`
	DeviceID string `json:"device_id,omitempty"`
}

type LoginResponseData struct {
	PlayerID string `json:"player_id"`
	Nickname string `json:"nickname"`
	Level    int    `json:"level"`
}

type PlayerInfoNotifyData struct {
	PlayerID string `json:"player_id"`
	Nickname string `json:"nickname"`
	Level    int    `json:"level"`
	Exp      int    `json:"exp"`
	Online   bool   `json:"online"`
}

type ItemEntry struct {
	ItemID int `json:"item_id"`
	Count  int `json:"count"`
}

type GetBagResponseData struct {
	Items []ItemEntry `json:"items"`
}

type UseItemRequestData struct {
	ItemID int `json:"item_id"`
	Count  int `json:"count"`
}

type UseItemResponseData struct {
	ItemID int `json:"item_id"`
	Used   int `json:"used"`
	Remain int `json:"remain"`
}

type ItemCountNotifyData struct {
	ItemID int `json:"item_id"`
	Count  int `json:"count"`
	Delta  int `json:"delta"`
}

type GatherRequestData struct {
	Resource string `json:"resource"`
	Amount   int    `json:"amount"`
}

type GatherResponseData struct {
	Resource string `json:"resource"`
	Gained   int    `json:"gained"`
}

type ResourceNotifyData struct {
	Gold    int `json:"gold"`
	Diamond int `json:"diamond"`
}
