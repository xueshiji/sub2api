package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetAccountPerformanceWindowStats 聚合 since 之后每个 (账号, 映射后的上游模型) 的性能指标：
// 平均 TTFT（只计 first_token_ms 非空的行）与 decode 吞吐的分子分母。
// decode 口径：只统计流式请求（duration_ms - first_token_ms），且时长有效、
// output_tokens > 0。非流式请求的 duration 含排队与首 token 等待，混入会随
// 各账号非流式占比漂移而污染 decode 分；非流式行的 first_token_ms 非空时仍
// 计入 TTFT 与样本数（当前仅个别平台的非流式路径记录该字段）。
// 同一请求模型在不同账号可映射到不同上游模型，按 upstream_model
// 分组才能让调度比较落在同质的工作负载上；upstream_model 缺失的历史行回落
// requested_model，两者皆空的行归入空桶，仅参与账号级聚合展示。
func (r *usageLogRepository) GetAccountPerformanceWindowStats(ctx context.Context, since time.Time) (*service.AccountPerfWindowStats, error) {
	query := `
WITH samples AS (
	SELECT
		account_id,
		COALESCE(NULLIF(upstream_model, ''), NULLIF(requested_model, ''), '') AS model,
		first_token_ms,
		output_tokens,
		CASE
			WHEN stream AND first_token_ms IS NOT NULL AND duration_ms IS NOT NULL
				AND duration_ms > first_token_ms AND output_tokens > 0
				THEN duration_ms - first_token_ms
			ELSE NULL
		END AS decode_ms
	FROM usage_logs
	WHERE created_at >= $1
)
SELECT
	account_id,
	model,
	COUNT(first_token_ms) AS sample_count,
	AVG(first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS avg_ttft_ms,
	COUNT(first_token_ms) AS ttft_count,
	COALESCE(SUM(output_tokens) FILTER (WHERE decode_ms IS NOT NULL), 0) AS sum_output_tokens,
	COALESCE(SUM(decode_ms), 0) AS sum_decode_ms
FROM samples
GROUP BY account_id, model`

	rows, err := r.sql.QueryContext(ctx, query, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := &service.AccountPerfWindowStats{
		Rows:          make([]service.AccountPerfWindowRow, 0, 16),
		PoolTTFTP95Ms: make(map[string]float64),
		PoolTTFTP50Ms: make(map[string]float64),
	}
	for rows.Next() {
		var row service.AccountPerfWindowRow
		var avgTTFT sql.NullFloat64
		if err := rows.Scan(
			&row.AccountID,
			&row.Model,
			&row.SampleCount,
			&avgTTFT,
			&row.TtftCount,
			&row.SumOutputTokens,
			&row.SumDecodeMs,
		); err != nil {
			return nil, err
		}
		if avgTTFT.Valid {
			v := avgTTFT.Float64
			row.AvgTTFTMs = &v
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 池内每模型请求级 TTFT P95 与 P50：慢惩罚的判定基线（P95 定阈值，P50 定
	// 画像保护下限）。模型维度与账号聚合口径一致（upstream 优先，requested 兜底），
	// 只计 first_token_ms 非空行。usage_logs 自身有 model 列，GROUP BY model 会
	// 绑定到表列而非下面的 COALESCE 别名，必须按位置分组。
	poolPercentileQuery := `
SELECT
	COALESCE(NULLIF(upstream_model, ''), NULLIF(requested_model, ''), '') AS model,
	percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms),
	percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms)
FROM usage_logs
WHERE created_at >= $1 AND first_token_ms IS NOT NULL
GROUP BY 1`
	percentileRows, err := r.sql.QueryContext(ctx, poolPercentileQuery, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = percentileRows.Close() }()
	for percentileRows.Next() {
		var model string
		var p50, p95 float64
		if err := percentileRows.Scan(&model, &p50, &p95); err != nil {
			return nil, err
		}
		result.PoolTTFTP50Ms[model] = p50
		result.PoolTTFTP95Ms[model] = p95
	}
	if err := percentileRows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
