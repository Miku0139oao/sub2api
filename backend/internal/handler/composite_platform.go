package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func ensureCompositeTargetPlatform(c *gin.Context, apiKey *service.APIKey, model string) {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return
	}
	if _, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
		return
	}
	if platform, ok := service.DetectModelPlatform(model); ok {
		c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
	}
}

func compositeTargetPlatformAllowed(c *gin.Context, apiKey *service.APIKey, model string, allowed ...string) bool {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return true
	}
	ensureCompositeTargetPlatform(c, apiKey, model)
	platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	if !ok {
		return false
	}
	for _, allowedPlatform := range allowed {
		if platform == allowedPlatform {
			return true
		}
	}
	return false
}

func compositeTargetPlatformResolved(c *gin.Context, apiKey *service.APIKey, model string) bool {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return true
	}
	ensureCompositeTargetPlatform(c, apiKey, model)
	_, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	return ok
}

func effectiveAPIKeyPlatform(c *gin.Context, apiKey *service.APIKey) string {
	if c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	return apiKey.Group.Platform
}

// compositeGatewayFamily maps a concrete platform to the gateway service that
// serves it. Platforms in the same family share the handler, the upstream
// request protocol and the response protocol, so a failover between them is a
// pure re-dispatch. Crossing families would require translating the request
// and response formats mid-request, which this feature deliberately does not
// do.
func compositeGatewayFamily(platform string) (domain.PlatformGateway, bool) {
	spec, ok := domain.LookupPlatform(platform)
	if !ok {
		return "", false
	}
	return spec.Gateway, true
}

// compositeCandidateSwitchable reports whether switching from the currently
// resolved platform to candidate is safe within this handler.
//
// The request already passed the composite admission checks and the body was
// rewritten for the current platform, so the candidate must be served by the
// same gateway family. Without this guard a chain such as
// openai -> anthropic would leave an Anthropic gateway handler forwarding a
// Chat Completions body to an Anthropic account.
func compositeCandidateSwitchable(c *gin.Context, candidate service.CompositeRouteCandidate) bool {
	if c == nil || c.Request == nil {
		return false
	}
	current, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	if !ok {
		return false
	}
	currentFamily, currentOK := compositeGatewayFamily(current)
	candidateFamily, candidateOK := compositeGatewayFamily(candidate.TargetPlatform)
	return currentOK && candidateOK && currentFamily == candidateFamily
}

// advanceCompositeCandidate advances a composite request to the next untried
// platform in its priority chain after the current platform's accounts are
// exhausted.
//
// tried holds the platform that just failed; the platforms already recorded in
// the request context are also treated as tried. It returns the candidate that
// was selected (the caller rewrites the upstream model with it) and false once
// the chain is drained, in which case the caller must surface the original
// error.
//
// The switch is only allowed before any semantic response bytes were written:
// once streaming started the client already saw data from the failed platform.
// writerSizeBeforeForward is the byte count captured immediately before the
// attempt that just failed.
func advanceCompositeCandidate(c *gin.Context, apiKey *service.APIKey, streamStarted bool, writerSizeBeforeForward int) (service.CompositeRouteCandidate, bool) {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil {
		return service.CompositeRouteCandidate{}, false
	}
	if apiKey.Group.Platform != service.PlatformComposite {
		return service.CompositeRouteCandidate{}, false
	}
	// Streaming already started: the response protocol belongs to the failed
	// platform and cannot be replayed against another provider.
	//
	// Raw Written() is not enough: the compact SSE keepalive commits the 200
	// header and writes comment bytes without any semantic output, and
	// account-level failover deliberately ignores those bytes. Compare against
	// the pre-attempt snapshot on the same adjusted scale so a heartbeat does
	// not silently disable the composite switch.
	if streamStarted {
		return service.CompositeRouteCandidate{}, false
	}
	if service.OpenAICompactKeepaliveAdjustedWrittenSize(c) != writerSizeBeforeForward {
		return service.CompositeRouteCandidate{}, false
	}
	ctx := c.Request.Context()
	candidates, ok := service.CompositeRouteCandidatesFromContext(ctx)
	if !ok {
		return service.CompositeRouteCandidate{}, false
	}
	// The platform currently in use is by definition attempted even when the
	// failover loop did not report it.
	currentPlatform, currentOK := service.ResolvedTargetPlatformFromContext(ctx)
	if currentOK {
		ctx = service.WithCompositeAttempt(ctx, currentPlatform)
	}
	// Candidates ahead of the current platform may not have been tried (the
	// winning route leads the chain only when endpoint specificity decided it,
	// not when priority did). Advancing must therefore walk forward from the
	// current platform and never fall back to an earlier, higher-priority
	// platform; otherwise the request would bounce between two platforms.
	start := 0
	if currentOK {
		for i, candidate := range candidates {
			if candidate.TargetPlatform == currentPlatform {
				start = i + 1
				break
			}
		}
	}
	for _, candidate := range candidates[start:] {
		if service.CompositePlatformAttempted(ctx, candidate.TargetPlatform) {
			continue
		}
		if !compositeCandidateSwitchable(c, candidate) {
			continue
		}
		ctx = service.WithCompositeAttempt(ctx, candidate.TargetPlatform)
		ctx = service.WithResolvedTargetPlatformOverride(ctx, candidate.TargetPlatform, candidate.UpstreamModel)
		c.Request = c.Request.WithContext(ctx)
		return candidate, true
	}
	c.Request = c.Request.WithContext(ctx)
	return service.CompositeRouteCandidate{}, false
}

func openAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey) (string, []service.ReasoningEffortMapping, string, bool) {
	if apiKey == nil || apiKey.Group == nil {
		return "", nil, "", false
	}
	if apiKey.Group.Platform != service.PlatformAnthropic && apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		return "", nil, "", false
	}
	effectivePlatform := effectiveAPIKeyPlatform(c, apiKey)
	if effectivePlatform != service.PlatformAnthropic && effectivePlatform != service.PlatformOpenAI {
		return "", nil, "", false
	}
	maxEffort, mappings := apiKey.Group.MaxReasoningEffort, apiKey.Group.ReasoningEffortMappings
	if effectivePlatform == service.PlatformAnthropic {
		maxEffort, mappings = anthropicCompatibleReasoningEffortPolicy(maxEffort, mappings)
	}
	return maxEffort, mappings, apiKey.Group.MaxReasoningEffortOverLimit, true
}

func anthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey) (string, []service.ReasoningEffortMapping, string, bool) {
	if apiKey == nil || apiKey.Group == nil {
		return "", nil, "", false
	}
	if apiKey.Group.Platform != service.PlatformAnthropic && apiKey.Group.Platform != service.PlatformComposite {
		return "", nil, "", false
	}
	if effectiveAPIKeyPlatform(c, apiKey) != service.PlatformAnthropic {
		return "", nil, "", false
	}
	maxEffort, mappings := anthropicCompatibleReasoningEffortPolicy(apiKey.Group.MaxReasoningEffort, apiKey.Group.ReasoningEffortMappings)
	return maxEffort, mappings, apiKey.Group.MaxReasoningEffortOverLimit, true
}

func anthropicCompatibleReasoningEffortPolicy(maxEffort string, mappings []service.ReasoningEffortMapping) (string, []service.ReasoningEffortMapping) {
	if service.NormalizeMaxReasoningEffort(maxEffort) == "minimal" {
		maxEffort = "low"
	}
	normalizedMappings := append([]service.ReasoningEffortMapping(nil), mappings...)
	for i := range normalizedMappings {
		if service.NormalizeMaxReasoningEffort(normalizedMappings[i].To) == "minimal" {
			normalizedMappings[i].To = "low"
		}
	}
	return maxEffort, normalizedMappings
}

func bindRequestedReasoningEffort(c *gin.Context, body []byte, model string) {
	if c == nil || c.Request == nil {
		return
	}
	effort := service.CanonicalRequestedReasoningEffort(body, model)
	if effort == nil {
		return
	}
	c.Request = c.Request.WithContext(service.WithRequestedReasoningEffort(c.Request.Context(), *effort))
}

func stampOpenAIRequestedReasoningEffort(result *service.OpenAIForwardResult, c *gin.Context) {
	if result == nil || result.RequestedReasoningEffort != nil {
		return
	}
	if c == nil || c.Request == nil {
		return
	}
	result.RequestedReasoningEffort = service.RequestedReasoningEffortFromContext(c.Request.Context())
}

func stampForwardRequestedReasoningEffort(result *service.ForwardResult, requested *string) {
	if result == nil || result.RequestedReasoningEffort != nil {
		return
	}
	result.RequestedReasoningEffort = requested
}

func applyOpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, body []byte) ([]byte, bool, error) {
	bindRequestedReasoningEffort(c, body, strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	maxEffort, mappings, overLimit, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return service.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

func respondOpenAIReasoningEffortPolicyError(c *gin.Context, err error, write func(*gin.Context, int, string, string)) {
	if c == nil || err == nil || write == nil {
		return
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	write(c, http.StatusForbidden, "permission_error", err.Error())
}

func applyAnthropicReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, body []byte) ([]byte, bool, error) {
	maxEffort, mappings, overLimit, ok := anthropicReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false, nil
	}
	return service.ApplyReasoningEffortPolicy(body, maxEffort, mappings, overLimit)
}

func bindOpenAIReasoningEffortPolicyForMessagesRequest(c *gin.Context, apiKey *service.APIKey, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	bindRequestedReasoningEffort(c, body, strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	isCNTarget := service.IsCNProvider(effectiveAPIKeyPlatform(c, apiKey))
	if isCNTarget {
		effort := gjson.GetBytes(body, "output_config.effort")
		if effort.Type != gjson.String || strings.TrimSpace(effort.String()) == "" {
			return
		}
	}
	maxEffort, mappings, overLimit, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return
	}
	if isCNTarget {
		c.Request = c.Request.WithContext(service.WithOpenAIReasoningEffortPolicy(c.Request.Context(), maxEffort, mappings, overLimit))
		return
	}
	c.Request = c.Request.WithContext(service.WithOpenAIReasoningEffortPolicyForMessages(c.Request.Context(), body, maxEffort, mappings, overLimit))
}
