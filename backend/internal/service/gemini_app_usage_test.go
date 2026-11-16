//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// geminiAppUsageLogRepoStub reuses the batch-usage stub and only overrides the
// window-stats query the Gemini App usage path needs.
type geminiAppUsageLogRepoStub struct {
	usageBatchLogRepoStub
	stats *usagestats.AccountStats
	err   error
	start time.Time
}

func (r *geminiAppUsageLogRepoStub) GetAccountWindowStats(_ context.Context, _ int64, startTime time.Time) (*usagestats.AccountStats, error) {
	r.start = startTime
	return r.stats, r.err
}

func TestGeminiAppUsageReportsLocalWindowsOnly(t *testing.T) {
	repo := &geminiAppUsageLogRepoStub{
		stats: &usagestats.AccountStats{
			Requests:     12,
			Tokens:       3456,
			Cost:         0.42,
			StandardCost: 0.5,
			UserCost:     0.21,
		},
	}
	svc := &AccountUsageService{usageLogRepo: repo}
	before := time.Now().Add(-geminiAppLocalUsageWindow)

	usage, err := svc.getGeminiAppUsage(context.Background(), &Account{ID: 42, Platform: PlatformGeminiApp})
	require.NoError(t, err)
	require.Equal(t, "local", usage.Source)
	require.NotNil(t, usage.UpdatedAt)
	require.NotNil(t, usage.GeminiAppLocalUsage)
	require.EqualValues(t, 12, usage.GeminiAppLocalUsage.Requests)
	require.EqualValues(t, 3456, usage.GeminiAppLocalUsage.Tokens)
	require.InDelta(t, 0.42, usage.GeminiAppLocalUsage.Cost, 1e-9)
	require.InDelta(t, 0.5, usage.GeminiAppLocalUsage.StandardCost, 1e-9)
	require.InDelta(t, 0.21, usage.GeminiAppLocalUsage.UserCost, 1e-9)

	// 24h window, allowing for the few milliseconds between the two calls.
	require.False(t, repo.start.Before(before), "window start %s must not predate %s", repo.start, before)
	require.WithinDuration(t, time.Now().Add(-geminiAppLocalUsageWindow), repo.start, time.Minute)
}

func TestGeminiAppUsageWithoutRepoReturnsEmptyLocalUsage(t *testing.T) {
	svc := &AccountUsageService{}

	usage, err := svc.getGeminiAppUsage(context.Background(), &Account{ID: 7, Platform: PlatformGeminiApp})
	require.NoError(t, err)
	require.Equal(t, "local", usage.Source)
	require.Nil(t, usage.GeminiAppLocalUsage)
}

func TestGeminiAppUsagePropagatesRepoErrors(t *testing.T) {
	repo := &geminiAppUsageLogRepoStub{err: errors.New("db down")}
	svc := &AccountUsageService{usageLogRepo: repo}

	usage, err := svc.getGeminiAppUsage(context.Background(), &Account{ID: 7, Platform: PlatformGeminiApp})
	require.Error(t, err)
	require.Nil(t, usage)
	require.Contains(t, err.Error(), "gemini app local usage")
}

// The dispatcher must never fall through to the Anthropic usage API for a
// Gemini App OAuth account: CanGetUsage() is true for every OAuth account, and
// the Anthropic endpoint would answer 401 for a Google access token.
func TestGeminiAppUsageDispatchDoesNotUseAnthropicFetcher(t *testing.T) {
	repo := &geminiAppUsageLogRepoStub{}
	svc := &AccountUsageService{usageLogRepo: repo}
	account := &Account{
		ID:       9,
		Platform: PlatformGeminiApp,
		Type:     AccountTypeOAuth,
	}

	usage, err := svc.getUsageForAccount(context.Background(), account, false)
	require.NoError(t, err)
	require.Equal(t, "local", usage.Source)
	require.NotNil(t, usage.UpdatedAt)
}
