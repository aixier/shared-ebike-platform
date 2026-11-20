// Package migrations 把 SQL 迁移打进可执行文件。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
