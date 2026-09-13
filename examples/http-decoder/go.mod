// http-decoder plugin dependencies (locked, based on plugins/go.mod.template).
// Monorepo 内部示例：经 replace 指向仓库内 SDK 子模块 ./sdk（对外发布时不带 replace，
// 直接 go get 已发布的 github.com/OwnSecurityGuard/gametrace/sdk）。
module http-decoder

go 1.25.5

require (
	github.com/OwnSecurityGuard/gametrace/sdk v0.9.0
	google.golang.org/grpc v1.71.0
)

replace github.com/OwnSecurityGuard/gametrace/sdk => ../../sdk

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/net v0.34.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.9 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
