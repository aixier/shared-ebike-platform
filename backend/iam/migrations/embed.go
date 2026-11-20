// Package migrations 把 SQL 迁移文件打进可执行文件。
package migrations

import "embed"

// FS 是全部迁移文件。
//
//go:embed *.sql
var FS embed.FS
