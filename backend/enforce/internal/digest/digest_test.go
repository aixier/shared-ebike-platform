package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// 摘要按契约拼接：证据编号|执法人员|取证时刻|经度,纬度(6 位小数)|扫码结果|照片摘要…
func TestEvidenceDigestFollowsContractFormula(t *testing.T) {
	in := Input{
		EvidenceID: "EV20261201001",
		OfficerID:  "AC0503",
		CapturedAt: "2026-12-01T12:00:00+08:00",
		Lng:        104.0616171,
		Lat:        30.6066,
		ScanResult: "无政府码",
		PhotoSHA256: []string{
			"aa", "bb",
		},
	}
	want := sum("EV20261201001|AC0503|2026-12-01T12:00:00+08:00|104.061617,30.606600|无政府码|aa|bb")
	if got := Evidence(in); got != want {
		t.Fatalf("摘要不符：got %s want %s", got, want)
	}
}

// 任一项变化摘要即不符
func TestEvidenceDigestChangesWhenAnyFieldChanges(t *testing.T) {
	base := Input{EvidenceID: "EV1", OfficerID: "AC1", CapturedAt: "2026-12-01T12:00:00+08:00", Lng: 104, Lat: 30, ScanResult: "码已失效", PhotoSHA256: []string{"p1"}}
	d0 := Evidence(base)
	mut := []func(*Input){
		func(i *Input) { i.EvidenceID = "EV2" },
		func(i *Input) { i.OfficerID = "AC2" },
		func(i *Input) { i.CapturedAt = "2026-12-01T12:00:01+08:00" },
		func(i *Input) { i.Lng = 104.000001 },
		func(i *Input) { i.Lat = 30.000001 },
		func(i *Input) { i.ScanResult = "码未启用" },
		func(i *Input) { i.PhotoSHA256 = []string{"p2"} },
		func(i *Input) { i.PhotoSHA256 = []string{"p1", "p1"} },
	}
	for n, m := range mut {
		c := base
		c.PhotoSHA256 = append([]string(nil), base.PhotoSHA256...)
		m(&c)
		if Evidence(c) == d0 {
			t.Fatalf("第 %d 项变化后摘要未变", n)
		}
	}
}

func TestFileDigest(t *testing.T) {
	if got := Bytes([]byte("abc")); got != sum("abc") {
		t.Fatalf("文件摘要不符：%s", got)
	}
}
