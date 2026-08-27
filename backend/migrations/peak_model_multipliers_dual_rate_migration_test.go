package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration232ConvertsLegacyPerModelNumbersToDualRateRules(t *testing.T) {
	content, err := FS.ReadFile("232_peak_model_multipliers_dual_rate.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// 仅转换仍是纯数字（旧格式）的条目，off_peak 以分组当前默认值兜底，保持迁移前后计费行为不变。
	require.Contains(t, sql, `jsonb_typeof(value) = 'number'`)
	require.Contains(t, sql, `jsonb_build_object('peak', value, 'off_peak', g.off_peak_rate_multiplier)`)
	// 已是对象形态的条目原样保留（幂等）。
	require.Contains(t, sql, "ELSE value")
	// 存在旧格式条目才执行 UPDATE（幂等：转换后再次运行为 no-op）。
	require.Contains(t, sql, `EXISTS ( SELECT 1 FROM jsonb_each(g.peak_model_multipliers) WHERE jsonb_typeof(value) = 'number' )`)
}
