package domain

// PeakModelMultiplierRule 分模型分时倍率：工作日高峰窗口内取 Peak，窗口外与周末取 OffPeak。
// 命中规则（精确名优先，其次最长前缀通配符）时两个字段整体优先于分组默认倍率。
type PeakModelMultiplierRule struct {
	Peak    float64 `json:"peak"`
	OffPeak float64 `json:"off_peak"`
}
