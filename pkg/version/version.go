// version.go — 构建版本信息（T14）。
//
// 三个变量均为包级 var（非 const），供构建时通过 -ldflags "-X" 注入：
//
//	go build -ldflags "-X gametrace/pkg/version.Version=v0.5.0 -X gametrace/pkg/version.Commit=abc1234" ./cmd/gt-pipeline
//
// 未注入时保留 dev 值：本地 go build / go run 出的二进制不带版本信息，
// 报告 "dev (unknown)"。各入口（gt-pipeline / gt-mcp / gt-agent）提供
// -version flag 打印后退出。
package version

// Version 是发布版本号，构建时注入 git tag（如 v0.5.0）。
var Version = "dev"

// Commit 是构建时的 git commit 短哈希。
var Commit = "unknown"

// BuildTime 是构建时间（RFC3339），构建时注入。空串时省略不展示，
// 用于本地 go build / go run 未注入的场景。
var BuildTime = ""

// String 返回人读版本串：如 "v0.5.0 (abc1234 2026-09-12T09:26:13Z)"，
// 未注入 BuildTime 时退化为 "dev (unknown)"。
func String() string {
	if BuildTime == "" {
		return Version + " (" + Commit + ")"
	}
	return Version + " (" + Commit + " " + BuildTime + ")"
}
