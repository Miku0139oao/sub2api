package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	// CompositeRouteReasonAmbiguous marks a model whose explicit account
	// mappings span several provider platforms.
	CompositeRouteReasonAmbiguous = "model is exposed by multiple provider platforms"
	// CompositeRouteReasonOAuthAmbiguous is the ambiguous case where a claimant
	// holds an OAuth / setup-token credential that only the OpenAI gateway can
	// carry, and the model-name fallback would also pick an OpenAI-gateway
	// platform. The unresolved request would land in the Anthropic gateway
	// handler and select those credentials, so this group misconfiguration must
	// be rejected instead of guessing a platform from the model name.
	CompositeRouteReasonOAuthAmbiguous = "model is exposed by multiple provider platforms including OAuth accounts"
)

// OAuthAmbiguous reports whether the request must be rejected because the
// cross-platform ownership involves OpenAI-gateway OAuth credentials.
func (d CompositeRouteDecision) OAuthAmbiguous() bool {
	return !d.Matched && d.Reason == CompositeRouteReasonOAuthAmbiguous
}

// AmbiguousMessage is the client-facing rejection for an OAuthAmbiguous decision.
func (d CompositeRouteDecision) AmbiguousMessage() string {
	return fmt.Sprintf("Model %q is mapped on multiple provider platforms in this group; configure an explicit composite route", d.PublicModel)
}

type CompositeRouteResolver struct {
	repo                   CompositeModelRouteRepository
	modelOwnershipResolver CompositeModelOwnershipResolver
}

func NewCompositeRouteResolver(repo CompositeModelRouteRepository) *CompositeRouteResolver {
	return &CompositeRouteResolver{repo: repo}
}

func (r *CompositeRouteResolver) SetModelOwnershipResolver(resolver CompositeModelOwnershipResolver) {
	if r != nil {
		r.modelOwnershipResolver = resolver
	}
}

// ListExactPublicModels returns enabled, concrete route IDs suitable for a model catalog.
func (r *CompositeRouteResolver) ListExactPublicModels(ctx context.Context, groupID int64, endpoint string, includeSystemOne bool) ([]string, error) {
	if r == nil || r.repo == nil || groupID <= 0 {
		return nil, nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(routes))
	for _, route := range routes {
		if route.Enabled && route.MatchType == CompositeRouteMatchExact &&
			(endpoint == "" || normalizeCompositeRouteEndpoint(route.Endpoint) == CompositeRouteEndpointAny || normalizeCompositeRouteEndpoint(route.Endpoint) == endpoint) {
			if model := strings.TrimSpace(route.PublicModel); model != "" {
				models = append(models, model)
			}
		}
	}
	if !includeSystemOne {
		models = filterSystemOneRouteModels(routes, models, endpoint)
	}
	return models, nil
}

func (r *CompositeRouteResolver) FilterCodexModels(ctx context.Context, groupID int64, models []string) ([]string, error) {
	if r == nil || r.repo == nil || groupID <= 0 {
		return models, nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	return filterSystemOneRouteModels(routes, models, CompositeRouteEndpointResponses), nil
}

func filterSystemOneRouteModels(routes []CompositeModelRoute, models []string, endpoint string) []string {
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		route, matched := matchCompositeRoute(routes, model, normalizeCompositeRouteEndpoint(endpoint))
		if !matched || route.TargetPlatform != PlatformTypeSafe {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func (r *CompositeRouteResolver) Resolve(ctx context.Context, groupID int64, model, endpoint string) (CompositeRouteDecision, error) {
	decision, routes, err := r.resolveWithRoutes(ctx, groupID, model, endpoint)
	if err != nil || !decision.Matched || decision.Route == nil || len(routes) == 0 {
		return decision, err
	}
	decision.Candidates = compositeRouteCandidates(routes, model, endpoint, *decision.Route)
	if len(decision.Candidates) > 1 {
		// 跨平台负载均衡：把本会话按 weight 抽中的平台提到链首。没有 weight
		// 或没有 session hash 时原样返回，行为与以往完全一致。
		if sessionHash, ok := CompositeLBSessionHashFromContext(ctx); ok {
			decision.Candidates = orderCompositeCandidatesForLoadBalancing(decision.Candidates, sessionHash, model)
			leading := decision.Candidates[0]
			if leading.TargetPlatform != decision.TargetPlatform {
				// 分流把另一个平台选为首选：同步 decision 的首选字段，让调用方
				// 看到的首选平台与候选链一致。
				decision.TargetPlatform = leading.TargetPlatform
				decision.UpstreamModel = leading.UpstreamModel
				decision.LoadBalanced = true
			}
		}
	}
	return decision, nil
}

// resolveWithRoutes runs the resolution ladder (explicit route, then account
// ownership, then the built-in detector) and hands back the raw route list so
// callers can derive the remaining failover chain without a second query.
func (r *CompositeRouteResolver) resolveWithRoutes(ctx context.Context, groupID int64, model, endpoint string) (CompositeRouteDecision, []CompositeModelRoute, error) {
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	decision := CompositeRouteDecision{
		GroupID:     groupID,
		PublicModel: model,
		Endpoint:    endpoint,
	}
	if model == "" {
		decision.Reason = "model is required"
		return decision, nil, nil
	}

	if r != nil && r.repo != nil && groupID > 0 {
		routes, err := r.repo.ListByGroup(ctx, groupID, false)
		if err != nil {
			return decision, nil, fmt.Errorf("list composite routes: %w", err)
		}
		if route, ok := matchCompositeRoute(routes, model, endpoint); ok {
			upstreamModel := compositeRouteUpstreamModel(route, model)
			return CompositeRouteDecision{
				Matched:        true,
				Source:         CompositeRouteSourceExplicit,
				GroupID:        groupID,
				PublicModel:    model,
				TargetPlatform: route.TargetPlatform,
				UpstreamModel:  upstreamModel,
				Endpoint:       endpoint,
				Route:          &route,
			}, routes, nil
		}
	}

	if r != nil && r.modelOwnershipResolver != nil && groupID > 0 {
		ownership, err := r.modelOwnershipResolver(ctx, groupID, model)
		if err != nil {
			// A recognizable model can still use the existing detector when the
			// account catalog is temporarily unavailable. Unknown aliases cannot.
			if _, detectable := DetectModelPlatform(model); !detectable {
				return decision, nil, fmt.Errorf("resolve account model ownership: %w", err)
			}
		} else if ownership.Ambiguous {
			decision.Reason = CompositeRouteReasonAmbiguous
			// 未解析的请求由下游 handler 按模型名前缀兜底选平台；只有兜底落在
			// OpenAI 网关族时，OpenAI 网关 OAuth 声明方才会被误送进 Anthropic 管线。
			if guessed, ok := DetectModelPlatform(model); ok && ownership.OpenAIGatewayOAuthClaimed && domain.UsesOpenAIGateway(guessed) {
				decision.Reason = CompositeRouteReasonOAuthAmbiguous
			}
			return decision, nil, nil
		} else if ownership.Matched {
			platform := strings.TrimSpace(ownership.TargetPlatform)
			if !isConcreteRequestPlatform(platform) {
				decision.Reason = "account model ownership has no concrete target platform"
				return decision, nil, nil
			}
			return CompositeRouteDecision{
				Matched:        true,
				Source:         CompositeRouteSourceAccount,
				GroupID:        groupID,
				PublicModel:    model,
				TargetPlatform: platform,
				UpstreamModel:  model,
				Endpoint:       endpoint,
			}, nil, nil
		}
	}

	if platform, ok := DetectModelPlatform(model); ok {
		return CompositeRouteDecision{
			Matched:        true,
			Source:         CompositeRouteSourceDetector,
			GroupID:        groupID,
			PublicModel:    model,
			TargetPlatform: platform,
			UpstreamModel:  model,
			Endpoint:       endpoint,
		}, nil, nil
	}
	decision.Reason = "no explicit route or built-in detector match"
	return decision, nil, nil
}

// compositeRouteUpstreamModel returns the model identifier forwarded upstream
// for a route: the configured upstream model, else the requested model.
func compositeRouteUpstreamModel(route CompositeModelRoute, requestedModel string) string {
	if upstreamModel := strings.TrimSpace(route.UpstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return requestedModel
}

// compositeRouteCandidates builds the ordered cross-platform failover chain
// rooted at the already-selected route.
//
// Only routes sharing the winner's public_model, match_type and effective
// endpoint are eligible — an `exact` route and a `prefix` route for the same
// alias describe different request shapes and must never be chained. Routes
// are ordered by the same comparator that picked the winner (endpoint
// specificity, then priority, then id), and only the highest-priority route per
// target platform survives, so a platform is never attempted twice in one
// request. The chain is returned only when it has more than one member.
func compositeRouteCandidates(routes []CompositeModelRoute, model, endpoint string, winner CompositeModelRoute) []CompositeRouteCandidate {
	if len(routes) == 0 {
		return nil
	}
	publicModel := strings.TrimSpace(winner.PublicModel)
	matchType := normalizeCompositeRouteMatchType(winner.MatchType)
	if publicModel == "" {
		return nil
	}

	type candidate struct {
		route          CompositeModelRoute
		endpointWeight int
	}
	chain := make([]candidate, 0, len(routes))
	for _, route := range routes {
		if strings.TrimSpace(route.PublicModel) != publicModel {
			continue
		}
		if normalizeCompositeRouteMatchType(route.MatchType) != matchType {
			continue
		}
		routeEndpoint := normalizeCompositeRouteEndpoint(route.Endpoint)
		if routeEndpoint != endpoint && routeEndpoint != CompositeRouteEndpointAny {
			continue
		}
		if !isConcreteRequestPlatform(strings.TrimSpace(route.TargetPlatform)) {
			continue
		}
		route.Endpoint = routeEndpoint
		route.MatchType = matchType
		chain = append(chain, candidate{
			route:          route,
			endpointWeight: boolToInt(routeEndpoint == endpoint),
		})
	}
	if len(chain) < 2 {
		return nil
	}

	sort.SliceStable(chain, func(i, j int) bool {
		a, b := chain[i], chain[j]
		if a.endpointWeight != b.endpointWeight {
			return a.endpointWeight > b.endpointWeight
		}
		if a.route.Priority != b.route.Priority {
			return a.route.Priority < b.route.Priority
		}
		return a.route.ID < b.route.ID
	})

	// The winner must lead the chain even if another route orders ahead of it
	// (for example an `any`-endpoint route with a lower priority that lost to
	// an endpoint-specific one). Candidates[0] therefore always matches the
	// platform this decision already selected.
	winnerID := winner.ID
	ordered := make([]candidate, 0, len(chain))
	for _, item := range chain {
		if item.route.ID == winnerID {
			ordered = append(ordered, item)
		}
	}
	for _, item := range chain {
		if item.route.ID != winnerID {
			ordered = append(ordered, item)
		}
	}

	seen := make(map[string]struct{}, len(ordered))
	candidates := make([]CompositeRouteCandidate, 0, len(ordered))
	for _, item := range ordered {
		platform := strings.TrimSpace(item.route.TargetPlatform)
		// The chain is platform-scoped: a second route to the same platform
		// cannot reach a different account pool, and account-level failover
		// already walks every account of that platform. Keeping the highest
		// priority row per platform avoids a duplicate, no-op attempt.
		if _, ok := seen[platform]; ok {
			continue
		}
		seen[platform] = struct{}{}
		candidates = append(candidates, CompositeRouteCandidate{
			TargetPlatform: platform,
			UpstreamModel:  compositeRouteUpstreamModel(item.route, model),
			Priority:       item.route.Priority,
			RouteID:        item.route.ID,
			Endpoint:       item.route.Endpoint,
			Weight:         item.route.Weight,
		})
	}
	if len(candidates) < 2 {
		return nil
	}
	return candidates
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func matchCompositeRoute(routes []CompositeModelRoute, model, endpoint string) (CompositeModelRoute, bool) {
	if len(routes) == 0 {
		return CompositeModelRoute{}, false
	}

	type candidate struct {
		route          CompositeModelRoute
		matchStrength  int
		endpointWeight int
		prefixLen      int
	}
	candidates := make([]candidate, 0, len(routes))
	for _, route := range routes {
		route.Endpoint = normalizeCompositeRouteEndpoint(route.Endpoint)
		if route.Endpoint != endpoint && route.Endpoint != CompositeRouteEndpointAny {
			continue
		}
		route.MatchType = normalizeCompositeRouteMatchType(route.MatchType)
		publicModel := strings.TrimSpace(route.PublicModel)
		if publicModel == "" {
			continue
		}

		matchStrength := 0
		prefixLen := len(publicModel)
		switch route.MatchType {
		case CompositeRouteMatchExact:
			if publicModel != model {
				continue
			}
			matchStrength = 2
		case CompositeRouteMatchPrefix:
			if !strings.HasPrefix(model, publicModel) {
				continue
			}
			matchStrength = 1
		default:
			continue
		}
		endpointWeight := 0
		if route.Endpoint == endpoint {
			endpointWeight = 1
		}
		candidates = append(candidates, candidate{
			route:          route,
			matchStrength:  matchStrength,
			endpointWeight: endpointWeight,
			prefixLen:      prefixLen,
		})
	}
	if len(candidates) == 0 {
		return CompositeModelRoute{}, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.matchStrength != b.matchStrength {
			return a.matchStrength > b.matchStrength
		}
		if a.endpointWeight != b.endpointWeight {
			return a.endpointWeight > b.endpointWeight
		}
		if a.prefixLen != b.prefixLen {
			return a.prefixLen > b.prefixLen
		}
		if a.route.Priority != b.route.Priority {
			return a.route.Priority < b.route.Priority
		}
		return a.route.ID < b.route.ID
	})
	return candidates[0].route, true
}
