// Package migrations 本服务的 SQL 迁移，embed 进可执行文件，启动时由 goose 执行。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
