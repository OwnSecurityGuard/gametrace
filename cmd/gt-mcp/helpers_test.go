package main

import (
	"gametrace/pkg/store"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// sqliteReaderOpener 返回一个使用 store.NewSQLiteStore 的 readerOpener，供测试注入。
func sqliteReaderOpener() func(dbPath, sessionID string) (captureReader, error) {
	return func(dbPath, sessionID string) (captureReader, error) {
		return store.NewSQLiteStore(dbPath)
	}
}

// contentText 从 CallToolResult 提取文本内容。
func contentText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}