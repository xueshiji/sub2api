package usagestats

// AccountStats 账号使用统计
//
// cost: 账号口径费用（使用 COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)）
// standard_cost: 标准费用（不含倍率；缓存命中率修正生效的行取售价口径的修正前成本 total_cost_before_adjustment，历史行回落 account_stats_cost，其余取 total_cost）
// user_cost: 用户/API Key 口径费用（使用 actual_cost，受分组倍率影响）
type AccountStats struct {
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	StandardCost float64 `json:"standard_cost"`
	UserCost     float64 `json:"user_cost"`
}
