//go:build integration

package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type IdentityCacheSuite struct {
	IntegrationRedisSuite
	cache *identityCache
}

func (s *IdentityCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewIdentityCache(s.rdb).(*identityCache)
}

func (s *IdentityCacheSuite) TestGetFingerprint_Missing() {
	fp, err := s.cache.GetFingerprint(s.ctx, 1)
	require.NoError(s.T(), err, "missing fingerprint is a miss, not a failure")
	require.Nil(s.T(), fp)
}

func (s *IdentityCacheSuite) TestSetAndGetFingerprint() {
	fp := &service.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.ctx, 1, fp), "SetFingerprint")
	gotFP, err := s.cache.GetFingerprint(s.ctx, 1)
	require.NoError(s.T(), err, "GetFingerprint")
	require.Equal(s.T(), "c1", gotFP.ClientID)
	require.Equal(s.T(), "ua", gotFP.UserAgent)
}

func (s *IdentityCacheSuite) TestFingerprint_TTL() {
	fp := &service.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.ctx, 2, fp))

	fpKey := fmt.Sprintf("%s%d", fingerprintKeyPrefix, 2)
	ttl, err := s.rdb.TTL(s.ctx, fpKey).Result()
	require.NoError(s.T(), err, "TTL fpKey")
	s.AssertTTLWithin(ttl, 1*time.Second, fingerprintTTL)
}

func (s *IdentityCacheSuite) TestGetFingerprint_JSONCorruption() {
	fpKey := fmt.Sprintf("%s%d", fingerprintKeyPrefix, 999)
	require.NoError(s.T(), s.rdb.Set(s.ctx, fpKey, "invalid-json-data", 1*time.Minute).Err(), "Set invalid JSON")

	fp, err := s.cache.GetFingerprint(s.ctx, 999)
	require.NoError(s.T(), err, "corrupted value is a miss so the create branch can overwrite it")
	require.Nil(s.T(), fp)
}

func (s *IdentityCacheSuite) TestSetFingerprint_Nil() {
	err := s.cache.SetFingerprint(s.ctx, 100, nil)
	require.NoError(s.T(), err, "SetFingerprint(nil) should succeed")
}

func TestIdentityCacheSuite(t *testing.T) {
	suite.Run(t, new(IdentityCacheSuite))
}
