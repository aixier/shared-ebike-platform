// Package migrations 把 SQL 迁移打进可执行文件，启动时由服务自己执行。
package migrations

import "embed"

// FS 全部迁移文件。
//
//go:embed *.sql
var FS embed.FS
