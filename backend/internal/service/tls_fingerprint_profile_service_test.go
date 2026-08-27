package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/stretchr/testify/require"
)

// tlsFingerprintProfileRepoStub 只实现 List：服务构造函数据此填充本地缓存，
// 其余方法被调用即失败。
type tlsFingerprintProfileRepoStub struct {
	profiles []*model.TLSFingerprintProfile
}

func (s *tlsFingerprintProfileRepoStub) List(ctx context.Context) ([]*model.TLSFingerprintProfile, error) {
	out := make([]*model.TLSFingerprintProfile, len(s.profiles))
	copy(out, s.profiles)
	return out, nil
}

func (s *tlsFingerprintProfileRepoStub) GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error) {
	panic("unexpected GetByID call")
}

func (s *tlsFingerprintProfileRepoStub) Create(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	panic("unexpected Create call")
}

func (s *tlsFingerprintProfileRepoStub) Update(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	panic("unexpected Update call")
}

func (s *tlsFingerprintProfileRepoStub) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

func TestResolveTLSProfile(t *testing.T) {
	svc := NewTLSFingerprintProfileService(&tlsFingerprintProfileRepoStub{profiles: []*model.TLSFingerprintProfile{
		{ID: 1, Name: "chrome-131", CipherSuites: []uint16{0x1301}},
		{ID: 2, Name: "firefox-135", CipherSuites: []uint16{0x1302}},
	}}, nil)

	tlsAccount := func(profileID any) *Account {
		extra := map[string]any{"enable_tls_fingerprint": true}
		if profileID != nil {
			extra["tls_fingerprint_profile_id"] = profileID
		}
		return &Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
			Extra:    extra,
		}
	}

	t.Run("未启用 TLS 指纹时返回 nil", func(t *testing.T) {
		require.Nil(t, svc.ResolveTLSProfile(&Account{
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{"enable_tls_fingerprint": false},
		}))
	})

	t.Run("绑定 profile id 时返回对应模板", func(t *testing.T) {
		p := svc.ResolveTLSProfile(tlsAccount(float64(2)))
		require.NotNil(t, p)
		require.Equal(t, "firefox-135", p.Name)
		require.Equal(t, []uint16{0x1302}, p.CipherSuites)
	})

	t.Run("profile id 为 -1 时返回模板库中的随机模板", func(t *testing.T) {
		for range 20 {
			p := svc.ResolveTLSProfile(tlsAccount(int64(-1)))
			require.NotNil(t, p)
			require.Contains(t, []string{"chrome-131", "firefox-135"}, p.Name)
		}
	})

	t.Run("启用但未绑定 profile 时返回内置默认", func(t *testing.T) {
		p := svc.ResolveTLSProfile(tlsAccount(nil))
		require.NotNil(t, p)
		require.Equal(t, "Built-in Default (Node.js 24.x)", p.Name)
		require.Empty(t, p.CipherSuites, "内置默认的指纹字段为空，由 dialer 回退到内置值")
	})

	t.Run("绑定的 profile id 不存在时回退内置默认", func(t *testing.T) {
		p := svc.ResolveTLSProfile(tlsAccount(int64(999)))
		require.NotNil(t, p)
		require.Equal(t, "Built-in Default (Node.js 24.x)", p.Name)
	})
}
