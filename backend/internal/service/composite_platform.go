package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// WithResolvedTargetPlatform stores the concrete provider chosen for a request
// made through a composite group.
func WithResolvedTargetPlatform(ctx context.Context, platform string) context.Context {
	platform = strings.TrimSpace(platform)
	if ctx == nil || platform == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
}

// ResolvedTargetPlatformFromContext returns the concrete provider chosen for
// the current request, if one was resolved.
func ResolvedTargetPlatformFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	platform, ok := ctx.Value(ctxkey.ResolvedTargetPlatform).(string)
	platform = strings.TrimSpace(platform)
	if !ok || platform == "" {
		return "", false
	}
	return platform, true
}

func WithCompositeRouteDecision(ctx context.Context, decision CompositeRouteDecision) context.Context {
	if ctx == nil || !decision.Matched {
		return ctx
	}
	ctx = WithResolvedTargetPlatform(ctx, decision.TargetPlatform)
	if model := strings.TrimSpace(decision.UpstreamModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, model)
	}
	if model := strings.TrimSpace(decision.PublicModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.RequestedPublicModel, model)
	}
	if source := strings.TrimSpace(decision.Source); source != "" {
		ctx = context.WithValue(ctx, ctxkey.CompositeRouteSource, source)
	}
	// Only a real chain overwrites the stored one. Re-resolving a public model
	// without an explicit multi-platform chain (detector, account ownership,
	// Gemini middleware) must not wipe a chain established earlier in the same
	// request.
	if len(decision.Candidates) > 1 {
		ctx = WithCompositeRouteCandidates(ctx, decision.Candidates)
	}
	return ctx
}

// WithCompositeRouteCandidates records the ordered cross-platform failover
// chain resolved for the current request. A nil or single-entry chain clears
// any previously stored chain so the request keeps single-platform behaviour.
func WithCompositeRouteCandidates(ctx context.Context, candidates []CompositeRouteCandidate) context.Context {
	if ctx == nil {
		return ctx
	}
	if len(candidates) < 2 {
		return context.WithValue(ctx, ctxkey.CompositeRouteCandidates, []CompositeRouteCandidate(nil))
	}
	stored := append([]CompositeRouteCandidate(nil), candidates...)
	return context.WithValue(ctx, ctxkey.CompositeRouteCandidates, stored)
}

// CompositeRouteCandidatesFromContext returns the failover chain resolved for
// the current composite request, if any.
func CompositeRouteCandidatesFromContext(ctx context.Context) ([]CompositeRouteCandidate, bool) {
	if ctx == nil {
		return nil, false
	}
	candidates, ok := ctx.Value(ctxkey.CompositeRouteCandidates).([]CompositeRouteCandidate)
	if !ok || len(candidates) < 2 {
		return nil, false
	}
	return candidates, true
}

// WithResolvedTargetPlatformOverride forcibly replaces the platform and
// upstream model chosen for the current request. Unlike
// WithCompositeRouteDecision it overwrites an existing decision, which is what
// cross-platform failover needs when it advances to the next candidate.
func WithResolvedTargetPlatformOverride(ctx context.Context, platform, upstreamModel string) context.Context {
	if ctx == nil {
		return ctx
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, nil)
		return ctx
	}
	// RequestedPublicModel is deliberately untouched: account_model ownership
	// checks compare the client's public alias against an account's model
	// mapping, which must not change just because the forwarded model did.
	ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, upstreamModel)
	return ctx
}

// WithCompositeAttempt appends a platform that has already been attempted for
// the current request. Repeated platforms are collapsed so the attempt list
// reads as the failover path taken.
func WithCompositeAttempt(ctx context.Context, platform string) context.Context {
	if ctx == nil {
		return ctx
	}
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return ctx
	}
	attempts := CompositeAttemptsFromContext(ctx)
	for _, attempted := range attempts {
		if attempted == platform {
			return ctx
		}
	}
	next := append(append([]string(nil), attempts...), platform)
	return context.WithValue(ctx, ctxkey.CompositeAttemptedPlatforms, next)
}

// CompositeAttemptsFromContext returns the platforms already attempted for the
// current request, in attempt order.
func CompositeAttemptsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	attempts, ok := ctx.Value(ctxkey.CompositeAttemptedPlatforms).([]string)
	if !ok || len(attempts) == 0 {
		return nil
	}
	return attempts
}

// CompositePlatformAttempted reports whether platform is in the request's
// attempted list.
func CompositePlatformAttempted(ctx context.Context, platform string) bool {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return false
	}
	for _, attempted := range CompositeAttemptsFromContext(ctx) {
		if attempted == platform {
			return true
		}
	}
	return false
}

func ResolvedUpstreamModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.ResolvedUpstreamModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func RequestedPublicModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.RequestedPublicModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func CompositeRouteSourceFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	source, ok := ctx.Value(ctxkey.CompositeRouteSource).(string)
	source = strings.TrimSpace(source)
	if !ok || source == "" {
		return "", false
	}
	return source, true
}

// DetectModelPlatform maps common public model IDs to the concrete provider
// platform used by sub2api. It intentionally returns false for ambiguous model
// names so composite groups fail closed instead of guessing.
func DetectModelPlatform(model string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}

	normalized = strings.TrimPrefix(normalized, "models/")
	if slash := strings.IndexByte(normalized, '/'); slash > 0 {
		provider := strings.TrimSpace(normalized[:slash])
		rest := strings.TrimSpace(normalized[slash+1:])
		switch provider {
		case "anthropic", "claude":
			return PlatformAnthropic, true
		case "openai", "chatgpt":
			return PlatformOpenAI, true
		case "google", "google-ai-studio", "gemini":
			return PlatformGemini, true
		case "xai", "x-ai", "grok":
			return PlatformGrok, true
		case "kimi", "moonshot":
			return PlatformKimi, true
		case "zhipu", "glm", "bigmodel":
			return PlatformZhipu, true
		case "deepseek":
			return PlatformDeepseek, true
		case "minimax":
			return PlatformMiniMax, true
		case "typesafe", "jev":
			return PlatformTypeSafe, true
		}
		if rest != "" {
			normalized = strings.TrimPrefix(rest, "models/")
		}
	}

	switch {
	case strings.HasPrefix(normalized, "anthropic.claude-"),
		strings.HasPrefix(normalized, "claude-"):
		return PlatformAnthropic, true
	case strings.HasPrefix(normalized, "gpt-"),
		strings.HasPrefix(normalized, "chatgpt-"),
		strings.HasPrefix(normalized, "codex-"),
		strings.HasPrefix(normalized, "text-embedding-"),
		strings.HasPrefix(normalized, "text-moderation-"),
		strings.HasPrefix(normalized, "omni-moderation-"),
		strings.HasPrefix(normalized, "dall-e-"),
		strings.HasPrefix(normalized, "gpt-image-"),
		strings.HasPrefix(normalized, "tts-"),
		strings.HasPrefix(normalized, "whisper-"),
		hasOpenAISeriesPrefix(normalized):
		return PlatformOpenAI, true
	// Gemini App 的原生别名必须先于 Gemini 的通用 gemini- 前缀判断，
	// 否则 composite 分组会把它们路由到 Gemini 平台。
	case normalized == "gemini-argon",
		normalized == "gemini-hydrogen",
		normalized == "gemini-flash-lite":
		return PlatformGeminiApp, true
	case strings.HasPrefix(normalized, "gemini-"),
		strings.HasPrefix(normalized, "learnlm-"):
		return PlatformGemini, true
	case normalized == "grok" || strings.HasPrefix(normalized, "grok-"):
		return PlatformGrok, true
	case normalized == "k3",
		normalized == "k3-256k",
		strings.HasPrefix(normalized, "kimi-"),
		strings.HasPrefix(normalized, "moonshot-"):
		return PlatformKimi, true
	case strings.HasPrefix(normalized, "glm-"):
		return PlatformZhipu, true
	case strings.HasPrefix(normalized, "deepseek-"):
		return PlatformDeepseek, true
	case strings.HasPrefix(normalized, "minimax-"),
		strings.HasPrefix(normalized, "abab5"),
		strings.HasPrefix(normalized, "abab6"),
		strings.HasPrefix(normalized, "abab7"):
		return PlatformMiniMax, true
	case normalized == "jev-latest" || strings.HasPrefix(normalized, "jev-"):
		return PlatformTypeSafe, true
	default:
		return "", false
	}
}

func hasOpenAISeriesPrefix(model string) bool {
	for _, prefix := range []string{"o1", "o3", "o4", "o5"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

func (s *GatewayService) resolveCompositeRouteDecision(ctx context.Context, group *Group, requestedModel, endpoint string) (CompositeRouteDecision, bool, error) {
	if group == nil || group.Platform != PlatformComposite {
		return CompositeRouteDecision{}, false, nil
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		upstreamModel := requestedModel
		if resolvedModel, modelOK := ResolvedUpstreamModelFromContext(ctx); modelOK {
			upstreamModel = resolvedModel
		}
		source := CompositeRouteSourceDetector
		if resolvedSource, sourceOK := CompositeRouteSourceFromContext(ctx); sourceOK {
			source = resolvedSource
		}
		// Keep the failover chain visible to callers that re-resolve from
		// context (scheduling, billing) so it survives the early return.
		candidates, _ := CompositeRouteCandidatesFromContext(ctx)
		return CompositeRouteDecision{
			Matched:        true,
			Source:         source,
			GroupID:        group.ID,
			PublicModel:    requestedModel,
			TargetPlatform: platform,
			UpstreamModel:  upstreamModel,
			Endpoint:       normalizeCompositeRouteEndpoint(endpoint),
			Candidates:     candidates,
		}, true, nil
	}
	decision, err := s.compositeResolver.Resolve(ctx, group.ID, requestedModel, endpoint)
	if err != nil {
		return decision, false, err
	}
	return decision, decision.Matched, nil
}

func isConcreteRequestPlatform(platform string) bool {
	return domain.IsConcretePlatform(platform)
}

// ClearStickySessionForCompositeFailover drops the OpenAI sticky session
// binding when a composite request moves to a different platform. Without this
// the scheduler keeps preferring the account bound on the failed platform, so
// the new platform's accounts are only reached after a wasted selection round.
//
// Non-composite groups and requests without a session hash are a no-op.
func (s *OpenAIGatewayService) ClearStickySessionForCompositeFailover(ctx context.Context, apiKey *APIKey, sessionHash string) {
	if s == nil || apiKey == nil || apiKey.GroupID == nil || strings.TrimSpace(sessionHash) == "" {
		return
	}
	if apiKey.Group == nil || apiKey.Group.Platform != PlatformComposite {
		return
	}
	_ = s.deleteStickySessionAccountID(ctx, apiKey.GroupID, sessionHash)
}

// WithCompositeAttemptBody stores the request body re-derived for the platform
// a composite failover just moved to. Handlers that rebuild their forward body
// from scratch on every loop iteration read it back so a model remap survives.
func WithCompositeAttemptBody(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.CompositeAttemptBody, body)
}

// CompositeAttemptBodyFromContext returns the body re-derived for the current
// composite failover attempt, if one has been recorded.
func CompositeAttemptBodyFromContext(ctx context.Context) ([]byte, bool) {
	if ctx == nil {
		return nil, false
	}
	body, ok := ctx.Value(ctxkey.CompositeAttemptBody).([]byte)
	if !ok || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// WithCompositeAttemptOriginalBody records the client's unmodified request body
// so a later composite failover can re-derive the upstream model from scratch
// instead of layering rewrites on an already-rewritten body.
func WithCompositeAttemptOriginalBody(ctx context.Context, body []byte) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.CompositeAttemptOriginalBody, body)
}

// CompositeAttemptOriginalBody returns the client's unmodified request body
// recorded for composite failover, or nil when none was recorded.
func CompositeAttemptOriginalBody(ctx context.Context) []byte {
	if ctx == nil {
		return nil
	}
	body, _ := ctx.Value(ctxkey.CompositeAttemptOriginalBody).([]byte)
	return body
}

// ClearCompositeStickySession drops the sticky session binding for a composite
// request that has moved to another platform, so selection does not keep
// preferring the account bound on the platform that just failed.
func (s *GatewayService) ClearCompositeStickySession(ctx context.Context, groupID int64, sessionHash string) {
	if s == nil || s.cache == nil || groupID == 0 || strings.TrimSpace(sessionHash) == "" {
		return
	}
	_ = s.cache.DeleteSessionAccountID(ctx, groupID, sessionHash)
}
