// Package content 本模块持有的自然语言内容：接入规范、联调环境的模拟数据、符合性用例目录。
// 内容改动只改 JSON 文件，不改代码。
package content

import (
	_ "embed"
	"encoding/json"
)

//go:embed spec.json
var specJSON []byte

//go:embed sandbox.json
var sandboxJSON []byte

//go:embed cases.json
var casesJSON []byte

// SpecSection 规范章节。
type SpecSection struct {
	No      string `json:"no"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Example string `json:"example,omitempty"`
}

// Spec 接入规范。
type Spec struct {
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	PublishedAt string        `json:"publishedAt"`
	Sections    []SpecSection `json:"sections"`
}

// SimVehicle 模拟车辆。
type SimVehicle struct {
	SimVehicleID string `json:"simVehicleId"`
	PlateNo      string `json:"plateNo"`
	PileTag      string `json:"pileTag"`
	PileTagType  string `json:"pileTagType"`
	Behavior     string `json:"behavior"`
}

// SimUser 模拟用户。
type SimUser struct {
	SimUserID string `json:"simUserId"`
	Kind      string `json:"kind"`
}

// Sandbox 联调环境的固定内容。
type Sandbox struct {
	Note        string       `json:"note"`
	SimPayment  string       `json:"simPayment"`
	SimVehicles []SimVehicle `json:"simVehicles"`
	SimUsers    []SimUser    `json:"simUsers"`
}

// CaseDef 符合性用例目录项。
type CaseDef struct {
	CaseID string `json:"caseId"`
	Group  string `json:"group"`
	Name   string `json:"name"`
}

// LoadSpec 读接入规范。
func LoadSpec() Spec {
	var s Spec
	mustUnmarshal(specJSON, &s)
	return s
}

// LoadSandbox 读联调环境内容。
func LoadSandbox() Sandbox {
	var s Sandbox
	mustUnmarshal(sandboxJSON, &s)
	return s
}

// LoadCases 读用例目录。
func LoadCases() []CaseDef {
	var c []CaseDef
	mustUnmarshal(casesJSON, &c)
	return c
}

func mustUnmarshal(b []byte, v any) {
	if err := json.Unmarshal(b, v); err != nil {
		panic("内容文件格式错误: " + err.Error())
	}
}
