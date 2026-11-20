// Package digest 计算证据防篡改摘要与照片文件摘要（契约 enforce.yaml 描述的公式）。
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Input 是参与证据摘要的全部字段。
type Input struct {
	EvidenceID  string
	OfficerID   string
	CapturedAt  string // RFC 3339 文本，原样参与计算
	Lng, Lat    float64
	ScanResult  string
	PhotoSHA256 []string // 按证据里照片的顺序
}

// Evidence 返回 SHA-256(证据编号|执法人员|取证时刻|经度,纬度|扫码结果|照片1摘要|…) 的十六进制。
func Evidence(in Input) string {
	parts := []string{
		in.EvidenceID,
		in.OfficerID,
		in.CapturedAt,
		fmt.Sprintf("%.6f,%.6f", in.Lng, in.Lat),
		in.ScanResult,
	}
	parts = append(parts, in.PhotoSHA256...)
	return Bytes([]byte(strings.Join(parts, "|")))
}

// Bytes 返回内容的 SHA-256 十六进制。
func Bytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
