package main

// notify.go 是探针的系统桌面通知能力（beeep 跨平台 Windows/Linux/Darwin）。
//
// 两个入口共用 sendSystemNotification：
//   - 远端：平台经控制流下发 Command_Notify（探针在 NAT 后，只能走这条下行通道）；
//   - 本地：POST /v1/notify，给坐在机器前的人/脚本自测用。
//
// 通知是一次性动作，不进 desired-state：探针离线就是下发失败，不做断线补发。

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gen2brain/beeep"
)

// notifyIcon 是通知图标（平台 logo 的 128px 缩略图）。烧进二进制而非运行期读盘：
// 探针是单文件下载产物，落地目录里不会有资源文件。
//
//go:embed notify_icon.png
var notifyIcon []byte

func init() {
	// 通知归属到「GameTrace Probe」这个名字下，避免各平台回退到通用调用器
	// （Linux 下不设置会显示 notify-send / DefaultAppName）。
	beeep.AppName = "GameTrace Probe"
}

// sendSystemNotification 弹一条系统通知。title 与 message 至少要有一个非空。
//
// icon 传 []byte：beeep 会落临时文件再交给各平台通知后端，用完即删。
// 注意不能传 nil —— beeep v0.11.2 三个平台的实现只接受 string / []byte。
func sendSystemNotification(title, message string) error {
	title = strings.TrimSpace(title)
	message = strings.TrimSpace(message)
	if title == "" && message == "" {
		return errors.New("notification has neither title nor message")
	}
	if title == "" {
		title = beeep.AppName
	}
	if err := beeep.Notify(title, message, notifyIcon); err != nil {
		return fmt.Errorf("send system notification: %w", err)
	}
	slog.Info("system notification shown", "title", title, "message", message)
	return nil
}
