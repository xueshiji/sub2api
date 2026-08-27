package dto

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAccountFromServiceShallow_TLSFingerprintEcho(t *testing.T) {
	t.Run("Anthropic API Key 启用时回传开关与模板 ID", func(t *testing.T) {
		src := &service.Account{
			ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Extra: map[string]any{
				"enable_tls_fingerprint":     true,
				"tls_fingerprint_profile_id": float64(7),
			},
		}

		got := AccountFromServiceShallow(src)
		require.NotNil(t, got.EnableTLSFingerprint)
		require.True(t, *got.EnableTLSFingerprint)
		require.NotNil(t, got.TLSFingerprintProfileID)
		require.EqualValues(t, 7, *got.TLSFingerprintProfileID)
	})

	t.Run("绑定随机模板(-1)时回传 -1", func(t *testing.T) {
		src := &service.Account{
			ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
			Extra: map[string]any{
				"enable_tls_fingerprint":     true,
				"tls_fingerprint_profile_id": float64(-1),
			},
		}

		got := AccountFromServiceShallow(src)
		require.NotNil(t, got.TLSFingerprintProfileID)
		require.EqualValues(t, -1, *got.TLSFingerprintProfileID)
	})

	t.Run("未启用时开关不回传，残留绑定仍回传", func(t *testing.T) {
		src := &service.Account{
			ID: 3, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Extra: map[string]any{
				"enable_tls_fingerprint":     false,
				"tls_fingerprint_profile_id": float64(7),
			},
		}

		got := AccountFromServiceShallow(src)
		require.Nil(t, got.EnableTLSFingerprint)
		require.NotNil(t, got.TLSFingerprintProfileID)
		require.EqualValues(t, 7, *got.TLSFingerprintProfileID)
	})

	t.Run("未绑定模板(0)时模板 ID 不回传", func(t *testing.T) {
		src := &service.Account{
			ID: 4, Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken,
			Extra: map[string]any{
				"enable_tls_fingerprint":     true,
				"tls_fingerprint_profile_id": float64(0),
			},
		}

		got := AccountFromServiceShallow(src)
		require.NotNil(t, got.EnableTLSFingerprint)
		require.True(t, *got.EnableTLSFingerprint)
		require.Nil(t, got.TLSFingerprintProfileID)
	})
}
