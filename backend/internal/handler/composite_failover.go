package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// shouldAdvanceCompositeOnError reports whether an upstream failure is worth
// retrying on another platform.
//
// Request-scoped failures (400/413/422, malformed payloads, unsupported
// parameters) are properties of the client request, not of the account or the
// provider: replaying them against another platform only burns the remaining
// chain and hides the real error. The existing failover classifier already
// decides this through NextAccountAction, so the composite switch reuses it
// rather than duplicating status-code lists.
func shouldAdvanceCompositeOnError(failoverErr *service.UpstreamFailoverError) bool {
	if failoverErr == nil {
		// No upstream error at all (for example the platform had no schedulable
		// account): another platform is exactly the right next step.
		return true
	}
	return failoverErr.ShouldRetryNextAccount()
}

// shouldAdvanceCompositeOnSelectionError is the equivalent gate for a failed
// account selection, which carries no UpstreamFailoverError.
//
// Only the compact contract is refused: "no account supports
// /responses/compact" is a per-request verdict every platform answers
// identically, and the handler reports it with a dedicated 503 code. Every
// other empty pool is exactly what the next platform exists for — including a
// model this group cannot serve, because the last platform's classification is
// what gets reported once the chain drains, so the client still receives the
// precise 404 rather than a generic upstream failure.
func shouldAdvanceCompositeOnSelectionError(err error, legacyCompact bool) bool {
	return !(legacyCompact && errors.Is(err, service.ErrNoAvailableCompactAccounts))
}

// advanceCompositePlatform moves a composite request to the next platform in
// its priority chain after the current platform's account pool is exhausted.
//
// It returns false when there is no next candidate, in which case the caller
// must surface the original error. When it returns true, reset has already
// been applied to the caller's per-attempt state: the new platform has a
// different account pool, so the failed account IDs, the switch budget and the
// previous platform's error all have to be cleared. Keeping them would let one
// platform's failures consume the next platform's budget, and a stale
// lastFailoverErr would report a platform's error that never happened there.
//
// The caller must already have verified that no response bytes were written.
func (h *OpenAIGatewayHandler) advanceCompositePlatform(
	c *gin.Context,
	apiKey *service.APIKey,
	streamStarted bool,
	compositeSwitches *int,
	writerSizeBeforeForward int,
	reset func(service.CompositeRouteCandidate),
) bool {
	candidates, hasCandidates := service.CompositeRouteCandidatesFromContext(c.Request.Context())
	if !hasCandidates {
		return false
	}
	// The chain is bounded by its own length; this is a hard stop against any
	// future refactor that forgets to mark a platform as attempted.
	if *compositeSwitches >= len(candidates) {
		return false
	}
	if _, started := service.ResolvedTargetPlatformFromContext(c.Request.Context()); !started {
		return false
	}
	candidate, ok := advanceCompositeCandidate(c, apiKey, streamStarted, writerSizeBeforeForward)
	if !ok {
		return false
	}
	*compositeSwitches++
	if reset != nil {
		reset(candidate)
	}
	return true
}

// advanceAnthropicFamilyCompositePlatform is the same step for the handlers
// that use FailoverState (Anthropic-family gateways and the OpenAI Messages
// bridge). It clears FailoverState's per-platform budget instead of the loose
// counters the OpenAI Responses/ChatCompletions handlers use.
func advanceAnthropicFamilyCompositePlatform(
	c *gin.Context,
	apiKey *service.APIKey,
	fs *FailoverState,
	streamStarted bool,
	compositeSwitches *int,
	writerSizeBeforeForward int,
	reset func(service.CompositeRouteCandidate),
) bool {
	candidates, hasCandidates := service.CompositeRouteCandidatesFromContext(c.Request.Context())
	if !hasCandidates {
		return false
	}
	if *compositeSwitches >= len(candidates) {
		return false
	}
	candidate, ok := advanceCompositeCandidate(c, apiKey, streamStarted, writerSizeBeforeForward)
	if !ok {
		return false
	}
	*compositeSwitches++
	fs.ResetForCompositePlatformSwitch()
	if reset != nil {
		reset(candidate)
	}
	return true
}

// advanceAnthropicCompositePlatform is the FailoverState-based switch for the
// Anthropic gateway handlers. It also rewrites the upstream model in the
// request body, since those handlers rebuild the forward body from the original
// client body on every loop iteration.
//
// The failed platform's sticky binding and prefetched sticky account are
// dropped: selection prefers a prefetch over the cache, so leaving either in
// place would keep steering the request at an account the new platform's gate
// rejects.
func (h *GatewayHandler) advanceAnthropicCompositePlatform(
	c *gin.Context,
	apiKey *service.APIKey,
	fs *FailoverState,
	streamStarted bool,
	compositeSwitches *int,
	writerSizeBeforeForward int,
	sessionHash string,
	channelMapping service.ChannelMappingResult,
	reqModel string,
) bool {
	if !shouldAdvanceCompositeOnError(fs.LastFailoverErr) {
		return false
	}
	return advanceAnthropicFamilyCompositePlatform(c, apiKey, fs, streamStarted, compositeSwitches, writerSizeBeforeForward, func(candidate service.CompositeRouteCandidate) {
		body := withCompositeCandidateModel(service.CompositeAttemptOriginalBody(c.Request.Context()), candidate.UpstreamModel, h.gatewayService.ReplaceModelInBody)
		body = openAIModelMappedBody(body, channelMapping.Mapped, channelMapping.MappedModel, h.gatewayService.ReplaceModelInBody)
		ctx := service.WithCompositeAttemptBody(c.Request.Context(), body)
		ctx = service.WithPrefetchedStickySession(ctx, 0, 0, h.metadataBridgeEnabled())
		if sessionHash != "" {
			if groupID := derefInt64Ptr(apiKey.GroupID); groupID != 0 {
				h.gatewayService.ClearCompositeStickySession(ctx, groupID, sessionHash)
			}
		}
		c.Request = c.Request.WithContext(ctx)
	})
}

func derefInt64Ptr(ptr *int64) int64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}

// ResetForCompositePlatformSwitch clears the account-level failover budget so
// the next platform starts with a full allowance and without the failed
// platform's account exclusions.
//
// ForceCacheBilling is cleared too: it is set when a sticky-bound session walks
// away from its account, which reprices input tokens as cache reads. A cold
// upstream on the new platform has no cache, so carrying the flag would
// mis-bill the new platform's first request.
func (s *FailoverState) ResetForCompositePlatformSwitch() {
	if s == nil {
		return
	}
	s.SwitchCount = 0
	s.FailedAccountIDs = make(map[int64]struct{})
	s.SameAccountRetryCount = make(map[int64]int)
	s.LastFailoverErr = nil
	s.profitVetoCount = 0
	s.profitVetoedAccountIDs = make(map[int64]struct{})
	s.ForceCacheBilling = false
}

// resetOpenAICompositeAttemptState clears the account-level failover state that
// must not carry into a different platform.
func resetOpenAICompositeAttemptState(
	failedAccountIDs map[int64]struct{},
	sameAccountRetryCount map[int64]int,
	switchCount *int,
	firstOutputTimeoutSwitchCount *int,
	profitVetoCount *int,
	lastFailoverErr **service.UpstreamFailoverError,
	oauth429FailoverState *service.OpenAIOAuth429FailoverState,
) {
	for accountID := range failedAccountIDs {
		delete(failedAccountIDs, accountID)
	}
	for accountID := range sameAccountRetryCount {
		delete(sameAccountRetryCount, accountID)
	}
	*switchCount = 0
	if firstOutputTimeoutSwitchCount != nil {
		*firstOutputTimeoutSwitchCount = 0
	}
	*profitVetoCount = 0
	*lastFailoverErr = nil
	if oauth429FailoverState != nil {
		// OAuth-429 follow-up tracking refers to the failed platform's accounts.
		*oauth429FailoverState = service.OpenAIOAuth429FailoverState{}
	}
}

// compositeCandidateForwardModel returns the model to route on for a candidate.
// Channel mapping keeps precedence over the route's upstream model, matching
// the first attempt's resolution order.
func compositeCandidateForwardModel(channelMapping service.ChannelMappingResult, reqModel string, candidate service.CompositeRouteCandidate) string {
	if channelMapping.Mapped {
		return openAIChannelForwardModel(channelMapping, reqModel)
	}
	if upstreamModel := candidate.UpstreamModel; upstreamModel != "" {
		return upstreamModel
	}
	return reqModel
}

// applyCompositeAttemptToOpenAIRequest rewires everything the OpenAI gateway
// handlers derive once per request from the resolved platform: the forwarded
// model context and the sticky session.
//
// The sticky session matters because selection prefers the bound account. A
// binding created on the failed platform would keep steering the request at an
// account the scheduler now filters out by platform, so it is dropped in favour
// of the new platform's own selection.
func (h *OpenAIGatewayHandler) applyCompositeAttemptToOpenAIRequest(
	c *gin.Context,
	apiKey *service.APIKey,
	sessionHash string,
	forwardModel string,
	legacyCompact bool,
) {
	if c == nil || c.Request == nil {
		return
	}
	c.Request = c.Request.WithContext(service.WithOpenAIForwardModel(
		c.Request.Context(),
		forwardModel,
		legacyCompact,
	))
	if h.gatewayService != nil && sessionHash != "" {
		h.gatewayService.ClearStickySessionForCompositeFailover(c.Request.Context(), apiKey, sessionHash)
	}
}

// openAICompositeAttempt carries everything the OpenAI gateway handlers
// re-derive per platform attempt. It is filled in once per resolved platform so
// the loops only read plain values.
type openAICompositeAttempt struct {
	ChannelMapping  service.ChannelMappingResult
	Body            []byte
	ForwardModel    string
	RequestPlatform string
	Capability      service.OpenAIEndpointCapability
	// previousResponseID is only valid on the OpenAI platform: continuation
	// state is owned by that provider and no other upstream can resolve it.
	PreviousResponseID string
}

// prepareOpenAICompositeAttempt re-resolves the per-platform values for a
// candidate: channel mapping (the channel lookup is platform-scoped), the
// request body's upstream model, the routed model, the scheduler platform and
// the endpoint capability.
//
// Capability is recomputed because it depends on the platform (a Responses
// capability required by OpenAI is not advertised by the CN providers), and
// the stale value would filter out every account of the new platform.
func (h *OpenAIGatewayHandler) prepareOpenAICompositeAttempt(
	c *gin.Context,
	apiKey *service.APIKey,
	originalBody []byte,
	reqModel string,
	candidate service.CompositeRouteCandidate,
	previousResponseID string,
	needsResponses bool,
	imageIntent bool,
) openAICompositeAttempt {
	ctx := c.Request.Context()
	mapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(ctx, apiKey.GroupID, reqModel)
	attempt := openAICompositeAttempt{
		ChannelMapping:  mapping,
		ForwardModel:    compositeCandidateForwardModel(mapping, reqModel, candidate),
		RequestPlatform: openAICompatibleRequestPlatform(ctx, apiKey),
		Capability:      openAIResponsesRequiredCapabilityForRequest(imageIntent, needsResponses, openAICompatibleRequestPlatform(ctx, apiKey)),
	}
	attempt.Body = rewriteCompositeAttemptBody(originalBody, mapping, candidate, h.gatewayService.ReplaceModelInBody)
	if attempt.RequestPlatform == service.PlatformOpenAI {
		attempt.PreviousResponseID = previousResponseID
	} else if strings.TrimSpace(previousResponseID) != "" {
		// Continuation ids are provider-owned. Forwarding one to a different
		// provider yields an opaque 400 and the id would be routed to an account
		// that never owned it, so drop it and let the request replay in full.
		attempt.Body = service.RemovePreviousResponseIDFromBody(attempt.Body)
	}
	return attempt
}

// rewriteCompositeAttemptBody re-derives the forwarded body for a candidate.
// Channel mapping is applied last so it keeps precedence over the route's
// upstream model, exactly as on the first attempt.
func rewriteCompositeAttemptBody(
	originalBody []byte,
	channelMapping service.ChannelMappingResult,
	candidate service.CompositeRouteCandidate,
	replace func([]byte, string) []byte,
) []byte {
	body := withCompositeCandidateModel(originalBody, candidate.UpstreamModel, replace)
	return openAIModelMappedBody(body, channelMapping.Mapped, channelMapping.MappedModel, replace)
}

// withCompositeCandidateModel rewrites the upstream model in a request body for
// the candidate about to be tried. An empty upstream model leaves the body
// untouched, mirroring route resolution where an empty upstream model forwards
// the requested model verbatim.
func withCompositeCandidateModel(body []byte, upstreamModel string, replace func([]byte, string) []byte) []byte {
	if upstreamModel == "" || replace == nil {
		return body
	}
	return replace(body, upstreamModel)
}
