package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_IsTLSFingerprintEnabled(t *testing.T) {
	t.Run("Anthropic OAuth 开启时生效", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Extra: map[string]any{
				"enable_tls_fingerprint": true,
			},
		}
		require.True(t, account.IsTLSFingerprintEnabled())
	})

	t.Run("Anthropic API Key 开启时生效", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				"enable_tls_fingerprint": true,
			},
		}
		require.True(t, account.IsTLSFingerprintEnabled())
	})

	t.Run("Anthropic Bedrock 开启时不生效", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeBedrock,
			Extra: map[string]any{
				"enable_tls_fingerprint": true,
			},
		}
		require.False(t, account.IsTLSFingerprintEnabled())
	})

	t.Run("非 Anthropic 平台开启时不生效", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				"enable_tls_fingerprint": true,
			},
		}
		require.False(t, account.IsTLSFingerprintEnabled())
	})

	t.Run("Extra 为空时关闭", func(t *testing.T) {
		account := &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
		}
		require.False(t, account.IsTLSFingerprintEnabled())
	})
}
