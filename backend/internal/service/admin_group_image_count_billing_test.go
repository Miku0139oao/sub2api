//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 管理端更新路径过去会把不适用的 image_count_billing 静默清成 false 并返回 200，
// 调用方无法得知配置没有生效。显式请求开启但平台/图片开关不支持时必须报错。
func TestUpdateGroupRejectsImageCountBillingOnUnsupportedTarget(t *testing.T) {
	tests := []struct {
		name          string
		platform      string
		allowImageGen bool
		wantErr       bool
	}{
		{name: "composite with image generation", platform: PlatformComposite, allowImageGen: true, wantErr: false},
		{name: "openai with image generation", platform: PlatformOpenAI, allowImageGen: true, wantErr: false},
		{name: "anthropic is unsupported", platform: PlatformAnthropic, allowImageGen: true, wantErr: true},
		{name: "gemini is unsupported", platform: PlatformGemini, allowImageGen: true, wantErr: true},
		{name: "image generation disabled", platform: PlatformOpenAI, allowImageGen: false, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &groupPlatformRepoStub{group: &Group{
				ID:                   7,
				Name:                 "g",
				Platform:             tt.platform,
				AllowImageGeneration: tt.allowImageGen,
			}}
			svc := &adminServiceImpl{groupRepo: repo}
			enabled := true

			got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{ImageCountBilling: &enabled})
			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			require.True(t, got.ImageCountBilling)
		})
	}
}

// 关闭开关永远允许，即使平台本身不支持，便于清理历史脏数据。
func TestUpdateGroupAllowsDisablingImageCountBillingAnywhere(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &Group{
		ID:                7,
		Name:              "g",
		Platform:          PlatformAnthropic,
		ImageCountBilling: true,
	}}
	svc := &adminServiceImpl{groupRepo: repo}
	disabled := false

	got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{ImageCountBilling: &disabled})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.ImageCountBilling)
}

// 切换平台后没重新声明开关时，仍按平台不适用清成 false（保持原语义）。
func TestUpdateGroupClearsImageCountBillingWhenPlatformBecomesUnsupported(t *testing.T) {
	repo := &groupPlatformRepoStub{group: &Group{
		ID:                   7,
		Name:                 "g",
		Platform:             PlatformOpenAI,
		AllowImageGeneration: true,
		ImageCountBilling:    true,
	}}
	svc := &adminServiceImpl{groupRepo: repo}

	got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{Platform: PlatformAnthropic})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.ImageCountBilling)
}
