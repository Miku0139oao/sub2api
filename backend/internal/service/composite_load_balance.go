package service

import (
	"context"
	"hash/fnv"
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// Composite load balancing
//
// Failover and load balancing answer different questions. Failover asks "this
// platform just failed, what next?" and is driven by `priority`. Load balancing
// asks "which platform should this conversation use in the first place?" and is
// driven by `weight`.
//
// The split is per session, not per request: prompt caches and
// `previous_response_id` are provider-owned, so a conversation bouncing between
// platforms would miss cache on every turn and, for continuation ids, fail
// outright. One session therefore keeps one primary platform for its whole
// life; `priority` still decides where it goes when that platform dies.

// WithCompositeLBSessionHash records the sticky session hash so composite route
// resolution can pin a session to a weighted platform. A request without a
// session hash (no client session id, no prompt cache key) falls back to
// priority-only ordering.
func WithCompositeLBSessionHash(ctx context.Context, sessionHash string) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, compositeLBSessionHashKey{}, strings.TrimSpace(sessionHash))
}

// CompositeLBSessionHashFromContext returns the sticky session hash recorded for
// load balancing, if any.
func CompositeLBSessionHashFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	hash, ok := ctx.Value(compositeLBSessionHashKey{}).(string)
	hash = strings.TrimSpace(hash)
	if !ok || hash == "" {
		return "", false
	}
	return hash, true
}

type compositeLBSessionHashKey struct{}

// CompositeLBSessionHeaderNames exposes the session headers the load-balancing
// seed reads, so the routing middleware can snapshot just those instead of the
// whole header map.
func CompositeLBSessionHeaderNames() []string {
	out := make([]string, len(explicitOpenAIHeaderSessionNames))
	copy(out, explicitOpenAIHeaderSessionNames)
	return out
}

// CompositeLBSessionSeed derives a stable per-conversation seed for load
// balancing without resolving the upstream model or the account pool.
//
// It is deliberately cheap enough for the routing middleware, which runs before
// any handler has parsed the request: an explicit session header wins, then the
// Claude Code `metadata.user_id` session id, then the OpenAI `prompt_cache_key`.
// Requests that carry none of these get no seed, which keeps load balancing off
// for them rather than scattering a stateless caller across platforms.
func CompositeLBSessionSeed(headers map[string]string, body []byte) string {
	for _, header := range explicitOpenAIHeaderSessionNames {
		if value := strings.TrimSpace(headers[header]); value != "" {
			return value
		}
	}
	if len(body) > 0 && gjson.ValidBytes(body) {
		if sessionID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); sessionID != "" {
			if parsed := ParseMetadataUserID(sessionID); parsed != nil && strings.TrimSpace(parsed.SessionID) != "" {
				return parsed.SessionID
			}
			return sessionID
		}
		if key := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); key != "" {
			return key
		}
		// Claude Messages carries the session in metadata.user_id; Live carries
		// it in session.session_id. Accept the latter as well.
		if sessionID := strings.TrimSpace(gjson.GetBytes(body, "session.session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// orderCompositeCandidatesForLoadBalancing promotes the session's weighted
// primary platform to the front of the chain and leaves every other candidate
// in its original priority order.
//
// It returns the chain unchanged when no candidate carries a positive weight or
// when the request has no session hash. Both cases keep the pre-existing
// "highest-priority route wins" behaviour, so a group that never sets weights
// is unaffected.
func orderCompositeCandidatesForLoadBalancing(candidates []CompositeRouteCandidate, sessionHash string, model string) []CompositeRouteCandidate {
	if len(candidates) < 2 {
		return candidates
	}
	primary, ok := pickWeightedCompositeCandidate(candidates, sessionHash, model)
	if !ok || primary == 0 {
		return candidates
	}
	ordered := make([]CompositeRouteCandidate, 0, len(candidates))
	ordered = append(ordered, candidates[primary])
	for i, candidate := range candidates {
		if i != primary {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

// pickWeightedCompositeCandidate returns the index of the session's primary
// candidate using weighted rendezvous hashing.
//
// Rendezvous hashing is used instead of `hash % totalWeight` because adding or
// removing a weighted platform then only remaps the sessions that belonged to
// that platform — a modulo bucket would reshuffle most sessions whenever the
// route table changed.
func pickWeightedCompositeCandidate(candidates []CompositeRouteCandidate, sessionHash string, model string) (int, bool) {
	sessionHash = strings.TrimSpace(sessionHash)
	if sessionHash == "" {
		return 0, false
	}
	hasWeight := false
	for _, candidate := range candidates {
		if candidate.Weight > 0 {
			hasWeight = true
			break
		}
	}
	if !hasWeight {
		return 0, false
	}

	bestIndex := -1
	bestScore := 0.0
	for i, candidate := range candidates {
		if candidate.Weight <= 0 {
			continue
		}
		score := compositeLBRendezvousScore(sessionHash, model, candidate.TargetPlatform, candidate.Weight)
		if bestIndex < 0 || score > bestScore {
			bestIndex = i
			bestScore = score
		}
	}
	if bestIndex < 0 {
		return 0, false
	}
	return bestIndex, true
}

// compositeLBRendezvousScore returns -weight/ln(h) where h is a deterministic
// hash of (session, model, platform) mapped into (0,1).
//
// With this score a platform's chance of winning is exactly
// weight/totalWeight, and changing one platform's weight only redistributes the
// sessions that platform used to own.
func compositeLBRendezvousScore(sessionHash, model, platform string, weight int) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(sessionHash))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(platform))

	// Map into (0,1) with enough resolution that ties are negligible. A zero
	// result would divide by zero in the log below; shift it to the smallest
	// representable positive unit.
	const scale = 1 << 53
	u := float64(h.Sum64()%scale) / scale
	if u <= 0 {
		u = 1 / scale
	}
	return -float64(weight) / math.Log(u)
}
