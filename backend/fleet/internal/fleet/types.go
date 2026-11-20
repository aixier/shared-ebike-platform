package fleet

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// CST 是对外输出时刻的时区（北京时间，RFC 3339 带 +08:00）。
var CST = time.FixedZone("CST", 8*3600)

func fmtTime(t time.Time) string { return t.In(CST).Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}

// parseTime 接受 RFC 3339，或原型数据里的 "YYYY-MM-DD HH:MM:SS"（北京时间）。
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02 15:04:05", s, CST)
}

// Actor 是操作人 {kind, id, name}。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func (a *Actor) valid() bool {
	if a == nil || strings.TrimSpace(a.ID) == "" {
		return false
	}
	switch a.Kind {
	case "人", "智能体", "系统", "用户":
		return true
	}
	return false
}

// reasonOK：事由至少 4 个字。
func reasonOK(s string) bool { return utf8.RuneCountInString(strings.TrimSpace(s)) >= 4 }

type Contact struct {
	Name  string `json:"name,omitempty"`
	Role  string `json:"role,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type Provider struct {
	ProviderID string   `json:"providerId"`
	Name       string   `json:"name"`
	Contact    *Contact `json:"contact,omitempty"`
}

type SettlementAccount struct {
	Bank        string `json:"bank,omitempty"`
	AccountName string `json:"accountName,omitempty"`
	Account     string `json:"account,omitempty"`
}

type Operator struct {
	OperatorID        string             `json:"operatorId"`
	Name              string             `json:"name"`
	ShortName         string             `json:"shortName"`
	Type              string             `json:"type"`
	AccessVia         string             `json:"accessVia"`
	Provider          *Provider          `json:"provider"`
	CreditCode        string             `json:"creditCode"`
	JoinedAt          *string            `json:"joinedAt"`
	Status            string             `json:"status"`
	ExitedAt          *string            `json:"exitedAt"`
	Contacts          []Contact          `json:"contacts"`
	SettlementAccount *SettlementAccount `json:"settlementAccount"`
	BrandIDs          []string           `json:"brandIds"`
}

type Brand struct {
	BrandID      string  `json:"brandId"`
	Name         string  `json:"name"`
	DisplayName  string  `json:"displayName"`
	Color        string  `json:"color"`
	VehicleType  string  `json:"vehicleType"`
	OperatorID   string  `json:"operatorId"`
	OperatorName string  `json:"operatorName"`
	AccessStatus string  `json:"accessStatus"`
	StatusSince  *string `json:"statusSince"`
	VehicleCount int     `json:"vehicleCount"`
	ProviderID   *string `json:"providerId"`
	PlatePrefix  string  `json:"platePrefix"`
}

type StatusChange struct {
	Status string  `json:"status"`
	At     string  `json:"at"`
	By     *Actor  `json:"by"`
	Note   *string `json:"note"`
}

type Pause struct {
	PauseID        string   `json:"pauseId"`
	BrandID        string   `json:"brandId"`
	PausedAt       string   `json:"pausedAt"`
	ResumedAt      *string  `json:"resumedAt"`
	Metric         string   `json:"metric"`
	Value          *float64 `json:"value"`
	Threshold      *float64 `json:"threshold"`
	SustainMinutes *int     `json:"sustainMinutes"`
	By             *Actor   `json:"by"`
	ResumedBy      *Actor   `json:"resumedBy"`
	Note           *string  `json:"note"`
	ResumeNote     *string  `json:"resumeNote"`
}

type BrandDetail struct {
	Brand
	StatusHistory []StatusChange `json:"statusHistory"`
	Pause         *Pause         `json:"pause"`
}

type AccessBrand struct {
	BrandDetail
	Operator     Operator       `json:"operator"`
	ByStatus     map[string]int `json:"byStatus"`
	ByCodeStatus map[string]int `json:"byCodeStatus"`
	Rentable     int            `json:"rentable"`
}

type AccessSummary struct {
	Status       string `json:"status"`
	BrandCount   int    `json:"brandCount"`
	VehicleCount int    `json:"vehicleCount"`
}

type AccessOverview struct {
	Summary []AccessSummary `json:"summary"`
	Brands  []AccessBrand   `json:"brands"`
}

type Vehicle struct {
	VehicleID     string  `json:"vehicleId"`
	BrandID       string  `json:"brandId"`
	BrandName     string  `json:"brandName"`
	OperatorID    string  `json:"operatorId"`
	OperatorName  string  `json:"operatorName"`
	PlateNo       string  `json:"plateNo"`
	GovCode       string  `json:"govCode"`
	PileTag       string  `json:"pileTag"`
	PileTagType   string  `json:"pileTagType"`
	VehicleType   string  `json:"vehicleType"`
	Model         string  `json:"model"`
	ListedAt      string  `json:"listedAt"`
	CodeIssuedAt  string  `json:"codeIssuedAt"`
	CodeStatus    string  `json:"codeStatus"`
	CodeEnableAt  *string `json:"codeEnableAt"`
	CodeInvalidAt *string `json:"codeInvalidAt"`
	Status        string  `json:"status"`
	Rentable      bool    `json:"rentable"`
}

type VehicleStats struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
	Pending int `json:"pending"`
	Invalid int `json:"invalid"`
}

type VehiclePage struct {
	Rows     []Vehicle    `json:"rows"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
	Stats    VehicleStats `json:"stats"`
}

// VehicleListRow 是车辆清单的一行。
type VehicleListRow struct {
	BrandID     string `json:"brandId"`
	PlateNo     string `json:"plateNo"`
	Model       string `json:"model"`
	PileTag     string `json:"pileTag"`
	PileTagType string `json:"pileTagType"`
}

type VehicleListResult struct {
	Added    int       `json:"added"`
	Updated  int       `json:"updated"`
	Vehicles []Vehicle `json:"vehicles"`
}

// VehiclePatch 是在线编辑的字段；nil 表示不改。
type VehiclePatch struct {
	Model       *string `json:"model"`
	PileTag     *string `json:"pileTag"`
	PileTagType *string `json:"pileTagType"`
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
