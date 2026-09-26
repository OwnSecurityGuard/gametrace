.PHONY: proto test build tidy

# 生成插件线上契约的 Go 代码。
# plugin.proto 是插件与宿主之间 gRPC 契约的 SSOT，只在本仓库生成一份：
# 宿主（gt）直接 import github.com/OwnSecurityGuard/gametrace/sdk/proto，
# 不再各自生成——两份生成代码会在 protobuf 全局注册表中登记同一个文件路径，
# 被链接进同一个二进制时直接 panic。
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/plugin.proto

test:
	go test ./...

build:
	go build ./...

tidy:
	go mod tidy
