//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAccountForCreateGeminiAppValidation(t *testing.T) {
	t.Run("oauth with refresh token is accepted", func(t *testing.T) {
		account, err := buildAccountForCreate(&CreateAccountInput{
			Name:     "gemini-app",
			Platform: PlatformGeminiApp,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"expires_at":    "1700000000",
			},
		}, map[string]any{})
		require.NoError(t, err)
		require.NotNil(t, account)
		require.Equal(t, PlatformGeminiApp, account.Platform)
		require.Equal(t, AccountTypeOAuth, account.Type)
		require.Equal(t, "refresh-token", account.GetCredential("refresh_token"))
		require.True(t, account.Schedulable)
		require.Equal(t, StatusActive, account.Status)
	})

	t.Run("apikey credentials are rejected", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "gemini-app",
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "key"},
		}, map[string]any{})
		require.EqualError(t, err, "gemini_app accounts only support oauth credentials")
	})

	t.Run("missing refresh token is rejected", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "gemini-app",
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "access-token"},
		}, map[string]any{})
		require.EqualError(t, err, "gemini_app accounts require a refresh_token")
	})

	t.Run("nil credentials are rejected", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:     "gemini-app",
			Platform: PlatformGeminiApp,
			Type:     AccountTypeOAuth,
		}, map[string]any{})
		require.EqualError(t, err, "gemini_app accounts require a refresh_token")
	})

	t.Run("blank refresh token is rejected", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "gemini-app",
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"refresh_token": "   "},
		}, map[string]any{})
		require.EqualError(t, err, "gemini_app accounts require a refresh_token")
	})

	t.Run("non string refresh token is rejected", func(t *testing.T) {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "gemini-app",
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"refresh_token": 123},
		}, map[string]any{})
		require.EqualError(t, err, "gemini_app accounts require a refresh_token")
	})

	t.Run("other platforms are unaffected", func(t *testing.T) {
		openAIAccount, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "openai",
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "access-token"},
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, PlatformOpenAI, openAIAccount.Platform)

		typeSafeAccount, err := buildAccountForCreate(&CreateAccountInput{
			Name:        "typesafe",
			Platform:    PlatformTypeSafe,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "key"},
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, PlatformTypeSafe, typeSafeAccount.Platform)
	})
}

// geminiAppUpdateAccountRepo 补上 spark 影子查询：把 type 改出 OAuth 时会先
// 检查是否存在影子账号，内嵌的 AccountRepository 为 nil 时调用会 panic。
type geminiAppUpdateAccountRepo struct {
	*upstreamBillingProbeAccountRepo
}

func (r *geminiAppUpdateAccountRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func newGeminiAppUpdateService(account *Account) *adminServiceImpl {
	repo := &geminiAppUpdateAccountRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{account.ID: account},
	}}
	return &adminServiceImpl{accountRepo: repo}
}

// 创建路径的不变量必须在更新路径（编辑 / 重新授权 / 批量修改共用）上同样成立，
// 否则账号会被写成无法转发的形态，且故障只会在真正调度时才暴露。
func TestUpdateAccountGeminiAppInvariants(t *testing.T) {
	newGeminiAppAccount := func() *Account {
		return &Account{
			ID:          501,
			Name:        "gemini-app",
			Platform:    PlatformGeminiApp,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
			},
		}
	}

	t.Run("oauth update keeps the refresh token", func(t *testing.T) {
		svc := newGeminiAppUpdateService(newGeminiAppAccount())

		updated, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{Name: "renamed"})
		require.NoError(t, err)
		require.Equal(t, "renamed", updated.Name)
		require.Equal(t, "refresh-token", updated.GetCredential("refresh_token"))
	})

	t.Run("switching to apikey is rejected", func(t *testing.T) {
		svc := newGeminiAppUpdateService(newGeminiAppAccount())

		_, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{Type: AccountTypeAPIKey})
		require.EqualError(t, err, "gemini_app accounts only support oauth credentials")
	})

	t.Run("clearing the refresh token is rejected", func(t *testing.T) {
		svc := newGeminiAppUpdateService(newGeminiAppAccount())

		_, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{
			Credentials: map[string]any{"refresh_token": ""},
		})
		require.EqualError(t, err, "gemini_app accounts require a refresh_token")
	})

	t.Run("stored apikey row is rejected", func(t *testing.T) {
		account := newGeminiAppAccount()
		account.Type = AccountTypeAPIKey
		svc := newGeminiAppUpdateService(account)

		_, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{Name: "renamed"})
		require.EqualError(t, err, "gemini_app accounts only support oauth credentials")
	})

	t.Run("other platforms are unaffected", func(t *testing.T) {
		account := newGeminiAppAccount()
		account.Platform = PlatformOpenAI
		svc := newGeminiAppUpdateService(account)

		updated, err := svc.UpdateAccount(context.Background(), 501, &UpdateAccountInput{
			Credentials: map[string]any{"refresh_token": ""},
		})
		require.NoError(t, err)
		require.Equal(t, PlatformOpenAI, updated.Platform)
	})
}
