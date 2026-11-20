// Package migrations 打包 bill 的 SQL 迁移，启动时由 goose 执行。
package migrations

import "embed"

// FS 全部迁移文件
//
//go:embed *.sql
var FS embed.FS
