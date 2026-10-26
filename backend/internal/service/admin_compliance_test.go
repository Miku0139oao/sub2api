package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type adminComplianceRepoStub struct {
	values map[string]string
}

func (r *adminComplianceRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	if value, ok := r.values[key]; ok {
		return &Setting{Key: key, Value: value}, nil
	}
	return nil, ErrSettingNotFound
}

func (r *adminComplianceRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *adminComplianceRepoStub) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *adminComplianceRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *adminComplianceRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	return nil
}

func (r *adminComplianceRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (r *adminComplianceRepoStub) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestAdminComplianceStatusRequiresAckWhenMissing(t *testing.T) {
	svc := NewSettingService(&adminComplianceRepoStub{}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, status.Required)
	require.Equal(t, AdminComplianceVersion, status.Version)
	require.Equal(t, AdminComplianceAckPhraseZH, status.AckPhraseZH)
	require.Equal(t, AdminComplianceAckPhraseZHTW, status.AckPhraseZHTW)
	require.Equal(t, AdminComplianceDocumentPathZH, status.DocumentPathZH)
	require.Equal(t, AdminComplianceDocumentPathZHTW, status.DocumentPathZHTW)
	require.Equal(t, AdminComplianceDocumentURLZHTW, status.DocumentURLZHTW)
}

func TestNormalizeAdminComplianceLanguage(t *testing.T) {
	cases := map[string]string{
		"zh-TW":                "zh-TW",
		"zh-tw":                "zh-TW",
		"ZH_TW":                "zh-TW",
		"zh-Hant":              "zh-TW",
		"zh-Hant-HK":           "zh-TW",
		"zh-HK":                "zh-TW",
		"zh-MO":                "zh-TW",
		"zh-TW,zh;q=0.9":       "zh-TW",
		"zh-TW;q=0.9,en;q=0.8": "zh-TW",
		"zh":                   "zh",
		"zh-CN":                "zh",
		"zh-Hans":              "zh",
		"en-US":                "en",
		"":                     "en",
	}
	for input, want := range cases {
		require.Equal(t, want, normalizeAdminComplianceLanguage(input), input)
	}
}

func TestAcceptAdminComplianceAcceptsTraditionalPhrase(t *testing.T) {
	repo := &adminComplianceRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	status, err := svc.AcceptAdminCompliance(context.Background(), AdminComplianceAcceptInput{
		AdminUserID: 7,
		Language:    "zh-TW",
		Phrase:      AdminComplianceAckPhraseZHTW,
	})
	require.NoError(t, err)
	require.False(t, status.Required)

	var stored AdminComplianceAcknowledgement
	require.NoError(t, json.Unmarshal([]byte(repo.values[adminComplianceAcknowledgementKey(7)]), &stored))
	require.Equal(t, "zh-TW", stored.Language)
	require.Equal(t, AdminComplianceDocumentPathZHTW, stored.DocumentZHTW)
}

func TestAcceptAdminComplianceRejectsCrossLocalePhrase(t *testing.T) {
	svc := NewSettingService(&adminComplianceRepoStub{}, &config.Config{})

	for _, input := range []AdminComplianceAcceptInput{
		{AdminUserID: 1, Language: "zh", Phrase: AdminComplianceAckPhraseZHTW},
		{AdminUserID: 1, Language: "zh-TW", Phrase: AdminComplianceAckPhraseZH},
	} {
		_, err := svc.AcceptAdminCompliance(context.Background(), input)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrAdminComplianceInvalidPhrase))
	}
}

func TestAcceptAdminComplianceRejectsWrongPhrase(t *testing.T) {
	svc := NewSettingService(&adminComplianceRepoStub{}, &config.Config{})

	_, err := svc.AcceptAdminCompliance(context.Background(), AdminComplianceAcceptInput{
		AdminUserID: 1,
		Language:    "zh",
		Phrase:      "我同意",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAdminComplianceInvalidPhrase))
}

func TestAcceptAdminCompliancePersistsCurrentVersion(t *testing.T) {
	repo := &adminComplianceRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	status, err := svc.AcceptAdminCompliance(context.Background(), AdminComplianceAcceptInput{
		AdminUserID: 42,
		Language:    "zh-CN",
		Phrase:      AdminComplianceAckPhraseZH,
		IPAddress:   "203.0.113.10",
		UserAgent:   "test-agent",
	})
	require.NoError(t, err)
	require.False(t, status.Required)
	require.NotNil(t, status.Acknowledgement)
	require.Equal(t, int64(42), status.Acknowledgement.AdminUserID)
	require.Equal(t, "203.0.113.10", status.Acknowledgement.IPAddress)

	var stored AdminComplianceAcknowledgement
	require.NoError(t, json.Unmarshal([]byte(repo.values[adminComplianceAcknowledgementKey(42)]), &stored))
	require.Equal(t, AdminComplianceVersion, stored.Version)
	require.Equal(t, AdminComplianceDocumentPathZH, stored.DocumentZH)
	require.Equal(t, AdminComplianceDocumentPathZHTW, stored.DocumentZHTW)
	require.Equal(t, "zh", stored.Language)
}

func TestAdminComplianceStatusRequiresAckOnOldVersion(t *testing.T) {
	old, err := json.Marshal(AdminComplianceAcknowledgement{Version: "v2026.01.01"})
	require.NoError(t, err)
	svc := NewSettingService(&adminComplianceRepoStub{
		values: map[string]string{adminComplianceAcknowledgementKey(1): string(old)},
	}, &config.Config{})

	status, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, status.Required)
	require.Nil(t, status.Acknowledgement)
}

func TestAdminComplianceStatusIsPerAdminUser(t *testing.T) {
	current, err := json.Marshal(AdminComplianceAcknowledgement{
		Version:     AdminComplianceVersion,
		AdminUserID: 1,
	})
	require.NoError(t, err)
	svc := NewSettingService(&adminComplianceRepoStub{
		values: map[string]string{adminComplianceAcknowledgementKey(1): string(current)},
	}, &config.Config{})

	statusForUserOne, err := svc.GetAdminComplianceStatus(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, statusForUserOne.Required)

	statusForUserTwo, err := svc.GetAdminComplianceStatus(context.Background(), 2)
	require.NoError(t, err)
	require.True(t, statusForUserTwo.Required)
}
