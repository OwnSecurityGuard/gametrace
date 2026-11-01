// http-decoder plugin dependencies (locked, based on plugins/go.mod.template).
// Monorepo 内部示例：经 replace 指向仓库内 SDK 子模块 ./sdk（对外发布时不带 replace，
// 直接 go get 已发布的 github.com/OwnSecurityGuard/gametrace/sdk）。
module http-decoder

go 1.26.0

require (
	github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
	google.golang.org/grpc v1.85.0
)

replace github.com/OwnSecurityGuard/gametrace/sdk => ../../sdk

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/gopacket/gopacket v1.7.2 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
