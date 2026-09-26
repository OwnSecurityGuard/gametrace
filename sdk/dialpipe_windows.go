//go:build windows

package sdk

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialNpipe 拨号 Windows named pipe。go-winio 只在 windows 构建下提供
// DialPipe，因此 npipe 端点的拨号按平台拆分（见 dialpipe_other.go）。
func dialNpipe(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipe(path, nil)
}
