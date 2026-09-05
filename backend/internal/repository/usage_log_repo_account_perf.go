package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetAccountPerformanceWindowStats 聚合 since 之后每个 (账号, 映射后的上游模型) 的性能指标：
// 平均 TTFT（只计 first_token_ms 非空的行）与 decode 吞吐的分子分母。
// decode 口径：流式请求取 duration_ms - first_token_ms，非流式取 duration_ms；
// 分子 SUM(output_tokens) 与分母 SUM(decode_ms) 只统计时长有效且 output_tokens > 0 的行，
// 避免异常时长或失败请求拉偏加权平均。
// sample_count 只统计 first_token_ms 或 decode_ms 至少一项有效的行，保证它表达
// 可参与评分的样本量。同一请求模型在不同账号可映射到不同上游模型，按 upstream_model
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
			WHEN NOT stream AND duration_ms IS NOT NULL AND duration_ms > 0 AND output_tokens > 0
				THEN duration_ms
			ELSE NULL
		END AS decode_ms
	FROM usage_logs
	WHERE created_at >= $1
)
SELECT
	account_id,
	model,
	COUNT(*) FILTER (WHERE first_token_ms IS NOT NULL OR decode_ms IS NOT NULL) AS sample_count,
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

	// 池内每模型请求级 TTFT P95：慢惩罚的判定基线。模型维度与账号聚合口径一致
	//（upstream 优先，requested 兜底），P95 只计 first_token_ms 非空行。
	// usage_logs 自身有 model 列，GROUP BY model 会绑定到表列而非下面的 COALESCE
	// 别名，必须按位置分组。
	p95Query := `
SELECT
	COALESCE(NULLIF(upstream_model, ''), NULLIF(requested_model, ''), '') AS model,
	percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms)
FROM usage_logs
WHERE created_at >= $1 AND first_token_ms IS NOT NULL
GROUP BY 1`
	p95Rows, err := r.sql.QueryContext(ctx, p95Query, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = p95Rows.Close() }()
	for p95Rows.Next() {
		var model string
		var p95 float64
		if err := p95Rows.Scan(&model, &p95); err != nil {
			return nil, err
		}
		result.PoolTTFTP95Ms[model] = p95
	}
	if err := p95Rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
