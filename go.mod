module gametrace

go 1.26.8

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/OwnSecurityGuard/gametrace/sdk v0.10.0
	github.com/expr-lang/expr v1.17.0
	github.com/gen2brain/beeep v0.11.2
	github.com/google/uuid v1.6.0
	github.com/gopacket/gopacket v1.7.2
	github.com/jackc/pgx/v5 v5.10.0
	github.com/mark3labs/mcp-go v1.1.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
	golang.org/x/sys v0.46.0
	google.golang.org/grpc v1.71.0
	google.golang.org/protobuf v1.36.9
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.54.0
)

// 契约单向流动（monorepo）：SDK 源码位于 ./sdk（独立 module，保留对外发布路径
// github.com/OwnSecurityGuard/gametrace/sdk），根模块经 replace 消费；外部插件仍按
// 已发布的 SDK 版本 go get 拉取。

// replace 指向仓库内 SDK 子模块，使其成为唯一的本地真源（对外发布经 Makefile
// sdk-publish 从 ./sdk 推送镜像仓库并打 tag）。
replace github.com/OwnSecurityGuard/gametrace/sdk => ./sdk

require (
	git.sr.ht/~jackmordaunt/go-toast v1.1.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/esiqveland/notify v0.13.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jackmordaunt/icns/v3 v3.0.1 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/sergeymakinen/go-bmp v1.0.0 // indirect
	github.com/sergeymakinen/go-ico v1.0.0-beta.0 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tadvi/systray v0.0.0-20190226123456-11a2b8fa57af // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
