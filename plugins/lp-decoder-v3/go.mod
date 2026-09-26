module lp-decoder-v3

go 1.25.5

require github.com/OwnSecurityGuard/gametrace/sdk v0.10.0

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/gopacket/gopacket v1.7.2 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/grpc v1.71.0 // indirect
	google.golang.org/protobuf v1.36.9 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// 该插件只依赖已发布的 github.com/OwnSecurityGuard/gametrace/sdk，不依赖 gametrace 源码树。
// 版本 v0.10.0 由脚手架单一事实来源固定（与开发指南/SDK 同版本发布）。
// 发布/获取 SDK：go get github.com/OwnSecurityGuard/gametrace/sdk@v0.10.0（或你们的模块代理）。
// 本地开发时临时加 replace 调试：go mod edit -replace github.com/OwnSecurityGuard/gametrace/sdk=<sdk路径>（monorepo 内为 ./sdk）
replace github.com/OwnSecurityGuard/gametrace/sdk => ../../sdk
