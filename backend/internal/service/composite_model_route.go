package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	CompositeRouteMatchExact  = "exact"
	CompositeRouteMatchPrefix = "prefix"

	CompositeRouteEndpointAny             = "any"
	CompositeRouteEndpointMessages        = "messages"
	CompositeRouteEndpointCountTokens     = "count_tokens"
	CompositeRouteEndpointResponses       = "responses"
	CompositeRouteEndpointChatCompletions = "chat_completions"
	CompositeRouteEndpointEmbeddings      = "embeddings"
	CompositeRouteEndpointImages          = "images"
	CompositeRouteEndpointGemini          = "gemini"

	CompositeRouteSourceExplicit = "route"
	CompositeRouteSourceDetector = "detector"
	CompositeRouteSourceAccount  = "account_model"
)

// CompositeModelOwnership identifies the concrete provider that exposes a
// public model through an account-level exact mapping.
type CompositeModelOwnership struct {
	TargetPlatform string
	Matched        bool
	Ambiguous      bool
	// OpenAIGatewayOAuthClaimed 表示声明方中含 OpenAI 网关族平台的 OAuth /
	// setup-token 凭据。歧义请求无法解析平台，会落到默认的 Anthropic 网关
	// handler：Anthropic / Gemini / Antigravity 的 OAuth 在那里有原生或兼容
	// 转发，API Key 上游可能兼容多种格式，唯独这类凭据只能走 OpenAI 网关。
	OpenAIGatewayOAuthClaimed bool
}

type CompositeModelOwnershipResolver func(context.Context, int64, string) (CompositeModelOwnership, error)

var (
	ErrCompositeRouteNotFound = infraerrors.NotFound("COMPOSITE_ROUTE_NOT_FOUND", "composite route not found")
	ErrCompositeRouteExists   = infraerrors.Conflict("COMPOSITE_ROUTE_EXISTS", "composite route already exists")
)

// CompositeModelRoute maps one public model identifier in a composite group to
// the concrete provider/model that should handle the request.
type CompositeModelRoute struct {
	ID             int64     `json:"id"`
	GroupID        int64     `json:"group_id"`
	PublicModel    string    `json:"public_model"`
	MatchType      string    `json:"match_type"`
	TargetPlatform string    `json:"target_platform"`
	UpstreamModel  string    `json:"upstream_model"`
	Endpoint       string    `json:"endpoint"`
	Priority       int       `json:"priority"`
	// Weight enables cross-platform load balancing for this route. Zero keeps
	// the route failover-only, which is the pre-existing behaviour.
	Weight         int       `json:"weight"`
	Enabled        bool      `json:"enabled"`
	Notes          string    `json:"notes"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CompositeRoutePreviewRequest struct {
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
}

// CompositeRouteCandidate is one link in the ordered failover chain for a
// public model. Several routes may share the same public_model and match_type
// while targeting different platforms; each is a fallback for the previous one
// within a single client request.
type CompositeRouteCandidate struct {
	TargetPlatform string `json:"target_platform"`
	UpstreamModel  string `json:"upstream_model"`
	Priority       int    `json:"priority"`
	RouteID        int64  `json:"route_id"`
	Endpoint       string `json:"endpoint"`
	// Weight is the route's load-balancing share. Zero means the platform is
	// failover-only and is never chosen as a session's primary platform.
	Weight int `json:"weight,omitempty"`
}

type CompositeRouteDecision struct {
	Matched        bool                 `json:"matched"`
	Source         string               `json:"source"`
	GroupID        int64                `json:"group_id"`
	PublicModel    string               `json:"public_model"`
	TargetPlatform string               `json:"target_platform"`
	UpstreamModel  string               `json:"upstream_model"`
	Endpoint       string               `json:"endpoint"`
	Route          *CompositeModelRoute `json:"route,omitempty"`
	Reason         string               `json:"reason,omitempty"`
	// Candidates is the ordered cross-platform failover chain for this public
	// model, starting with the platform this session should use. It is populated
	// only for explicit route matches with more than one distinct target
	// platform; a single route leaves it empty so request behaviour is
	// unchanged for every pre-existing configuration.
	Candidates []CompositeRouteCandidate `json:"candidates,omitempty"`
	// LoadBalanced reports that weighted load balancing moved this session off
	// the highest-priority platform, i.e. TargetPlatform came from `weight`
	// rather than from `priority`.
	LoadBalanced bool `json:"load_balanced,omitempty"`
}

type CompositeRouteInput struct {
	PublicModel    string
	MatchType      string
	TargetPlatform string
	UpstreamModel  string
	Endpoint       string
	Priority       int
	Weight         int
	Enabled        bool
	Notes          string
}

type CompositeModelRouteRepository interface {
	ListByGroup(ctx context.Context, groupID int64, includeDisabled bool) ([]CompositeModelRoute, error)
	Create(ctx context.Context, route *CompositeModelRoute) error
	Update(ctx context.Context, route *CompositeModelRoute) error
	Delete(ctx context.Context, id int64) error
	DeleteByGroup(ctx context.Context, groupID int64) error
}

func normalizeCompositeRouteEndpoint(endpoint string) string {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if endpoint == "" {
		return CompositeRouteEndpointAny
	}
	switch endpoint {
	case CompositeRouteEndpointMessages,
		CompositeRouteEndpointCountTokens,
		CompositeRouteEndpointResponses,
		CompositeRouteEndpointChatCompletions,
		CompositeRouteEndpointEmbeddings,
		CompositeRouteEndpointImages,
		CompositeRouteEndpointGemini:
		return endpoint
	default:
		return CompositeRouteEndpointAny
	}
}

func normalizeCompositeRouteMatchType(matchType string) string {
	matchType = strings.ToLower(strings.TrimSpace(matchType))
	switch matchType {
	case CompositeRouteMatchPrefix:
		return CompositeRouteMatchPrefix
	default:
		return CompositeRouteMatchExact
	}
}

func normalizeCompositeRouteInput(input CompositeRouteInput) CompositeRouteInput {
	input.PublicModel = strings.TrimSpace(input.PublicModel)
	input.MatchType = normalizeCompositeRouteMatchType(input.MatchType)
	input.TargetPlatform = strings.TrimSpace(input.TargetPlatform)
	input.UpstreamModel = strings.TrimSpace(input.UpstreamModel)
	input.Endpoint = normalizeCompositeRouteEndpoint(input.Endpoint)
	// 仅对 exact 路由把空 upstream_model 回填成 public_model：exact 命中时请求模型
	// 恒等于 public_model，回填只影响持久化/后台展示，保留原有契约不变。
	// prefix 路由留空则不回填——Resolve 会回退到具体请求模型，从而透传原始模型
	// （否则 public=deepseek-v4 的前缀路由会把 deepseek-v4-flash / deepseek-v4-pro
	// 都塌缩成固定的 deepseek-v4）。显式填写 upstream_model 时任何模式都原样固定转发。
	if input.UpstreamModel == "" && input.MatchType == CompositeRouteMatchExact {
		input.UpstreamModel = input.PublicModel
	}
	// A negative weight is meaningless; clamp instead of persisting it. Zero is
	// kept as "failover-only".
	if input.Weight < 0 {
		input.Weight = 0
	}
	input.Notes = strings.TrimSpace(input.Notes)
	return input
}
