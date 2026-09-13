//go:build !windows

package sdk

import (
	"context"
	"fmt"
	"net"
)

// dialNpipe 在非 Windows 平台拒绝 npipe 端点：named pipe 是 Windows-only
// 传输，宿主在 Linux 上应以 unix socket 或 TCP 暴露 registry 端点。
func dialNpipe(ctx context.Context, path string) (net.Conn, error) {
	return nil, fmt.Errorf("npipe endpoint %q is only supported on windows", path)
}
