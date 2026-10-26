# Composite Groups

Composite groups are an admin routing layer for API keys that should choose a
concrete provider from the requested model instead of binding the key to a
single provider group. They support both built-in model detection and an
admin-configured model route registry for public model aliases.

## Supported Providers

Composite groups can route to these concrete account platforms:

- Anthropic
- Gemini
- OpenAI
- Antigravity
- Grok
- Kimi
- Zhipu GLM
- DeepSeek
- MiniMax

The selected concrete platform is used for account selection, user platform
quota checks, post-usage billing, ops error platform attribution, channel
mapping/pricing lookup, and platform usage reporting.

## Route Registry

Admins can configure routes on a composite group from the group list's
`Routes` action or through the admin API:

- `GET /api/v1/admin/groups/:id/composite-routes`
- `POST /api/v1/admin/groups/:id/composite-routes`
- `PUT /api/v1/admin/groups/:id/composite-routes/:route_id`
- `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id`
- `POST /api/v1/admin/groups/:id/composite-routes/preview`

Each route belongs to one composite group and contains:

- `public_model`: model identifier the client sends.
- `match_type`: `exact` or `prefix`.
- `target_platform`: concrete provider platform.
- `upstream_model`: model identifier sent upstream. If omitted, the public
  model is reused.
- `endpoint`: `any`, `messages`, `count_tokens`, `responses`,
  `chat_completions`, `embeddings`, `images`, or `gemini`.
- `priority`: lower values win after match specificity.
- `weight`: cross-platform load-balancing share. `0` (default) keeps the route
  failover-only.
- `enabled`: disabled routes are ignored by runtime resolution but remain
  visible to admins.

Resolution order is explicit route first, then built-in detection. When more
than one explicit route matches, exact matches beat prefix matches,
endpoint-specific routes beat `any`, longer prefixes beat shorter prefixes,
then lower `priority`, then lower route id.

## Cross-Platform Priority Failover

Several routes may share the same `public_model` and `match_type` while
targeting different platforms. They form an ordered **candidate chain** for one
public model, so a single client request can try the next platform when the
current one fails — the client never has to switch aliases.

```
public_model = deepseek-v4.1-flash
  priority 10  opencode_go   (account 1)
  priority 20  command_code  (account 3)
  priority 30  deepseek      (account 6)
```

A route is only chained with routes that share its `public_model`,
`match_type` and compatible `endpoint`:

- `exact` and `prefix` routes never mix. They describe different request
  shapes, so `claude-` (prefix) is not a fallback for `claude-sonnet-4`
  (exact).
- Route `endpoint` must match the request endpoint, or be `any`.
- Candidates are ordered by the same comparator that picks the winner:
  endpoint specificity, then `priority`, then route id.
- Only the highest-priority route per target platform is kept. A second route to
  the same platform cannot reach a different account pool, and account-level
  failover already walks every account of that platform.
- The chain is only recorded when it has at least two platforms. A single route
  keeps exactly the previous behaviour.

The winning route always leads the chain, and failover only ever moves forward
from the platform currently in use.

## Cross-Platform Load Balancing

`priority` answers "what do we try after this platform fails". `weight` answers
"which platform should this conversation use in the first place". They are
independent, and a route can use both.

Set `weight` on the routes that should share traffic:

```
public_model = deepseek-v4.1-flash
  priority 10  weight 80   opencode_go
  priority 20  weight 20   command_code
  priority 30  weight 0    deepseek        ← failover only
```

Sessions are then split 80/20 between `opencode_go` and `command_code`;
`deepseek` receives traffic only when both of those fail.

### Why the split is per session, not per request

Prompt caches and `previous_response_id` are owned by the upstream provider.
A conversation that bounces between platforms each turn would miss cache on
every request, and continuation ids would be rejected outright by a platform
that never created them. Load balancing therefore pins one **session** to one
primary platform for its whole life.

A session is identified by, in order: the explicit session header
(`session-id`, `session_id`, `conversation_id`, and the OpenCode / CodeBuddy
equivalents), the Claude Code `metadata.user_id` session id, the OpenAI
`prompt_cache_key`, or the Live `session.session_id`. Requests carrying none of
these are **not** load balanced — they follow plain `priority` order rather than
being scattered across platforms, which would break their cache for no benefit.

### Distribution

Selection uses weighted rendezvous hashing. Each candidate scores
`-weight / ln(h(session, model, platform))` and the highest score wins, which
makes a platform's share exactly `weight / totalWeight`. Rendezvous hashing also
means that adding, removing or re-weighting one platform only remaps the
sessions that platform used to own, instead of reshuffling everything.

Failover still applies after the split: if the session's primary platform is
exhausted, the request moves along the chain in `priority` order, skipping
platforms it has already tried. A `weight: 0` route is never chosen as a
primary platform but stays in the chain as a failover target.

### Interaction with gateway families

The same gateway-family restriction applies: a session is only moved between
platforms served by the same gateway (`openai`, `anthropic`, `gemini`,
`antigravity`). Weights on candidates outside that family are ignored for the
request that entered through a different family.

### Inspecting the decision

The preview endpoint returns `load_balanced: true` when the weighted split moved
this session off the highest-priority platform, together with the resulting
`candidates` order. `weight` is included on each candidate.

### When a platform is abandoned

The request advances to the next platform when the current platform has no
accounts left for this request:

- the upstream returned a retryable failure (5xx, 429, 403, connection error), or
- the platform has no schedulable account at all, including when the last
  account was just excluded.

Request-scoped failures such as 400/413/422 do **not** advance the chain: they
are properties of the client request, and replaying them against another
provider only hides the real error. The existing failover classifier decides
this, so the composite switch reuses that verdict. The one platform-capability
case treated the same way is `/responses/compact` support, which has its own
client-facing 503 code.

`previous_response_id` is provider-owned. When a request carries one and the
switch leaves the OpenAI platform, the id is stripped from the upstream body so
the request replays in full instead of being sent to an upstream that never
owned it.

### What a switch changes

- The resolved target platform and upstream model for the request.
- The request body's `model` field, re-derived from the original client body.
  Channel model mapping is re-resolved for the new platform (the channel lookup
  is platform-scoped) and still takes precedence over the route's
  `upstream_model`.
- The upstream model used for account selection, and the scheduling capability
  when it depends on the platform (for example Responses support).
- The account-level failover budget, the excluded-account set and the
  per-account retry counters, which are reset because the new platform has a
  different account pool.
- The sticky session binding and any prefetched sticky account, which are
  dropped so selection does not keep preferring the account that just failed.

The client's public alias stays available for account-model ownership checks
after the switch.

### Same gateway family only

A switch stays inside one gateway family (`domain.PlatformSpec.Gateway`). The
request body, upstream protocol and response protocol are fixed by the handler
that accepted the request, so a chain such as `openai → anthropic` would send an
OpenAI-shaped body to an Anthropic account. Candidates in another family are
skipped; when none remain the chain counts as exhausted and the original error
is returned.

This matters in practice because `opencode_go`, `command_code`, `deepseek`,
`kimi`, `zhipu`, `minimax`, `grok` and `openai` all share the OpenAI gateway,
so chains between them work. A chain that mixes `openai` and `anthropic` will
only ever use the candidates of the family the request entered through.

### Billing and observability

Usage is recorded against the account that actually served the request, so
`usage_logs.platform`, `account_id` and `upstream_model` reflect the platform
that answered — not the first one that failed.

The admin preview endpoint
(`POST /api/v1/admin/groups/:id/composite-routes/preview`) returns the resolved
`candidates` list so the effective order can be reviewed before deploying.

### Not supported

- Cross-platform retry after streaming has started. Once response bytes were
  sent the client already received data from the failed platform, so the error
  is returned instead of replaying the request.
- Polling several upstream models within one platform. The chain is keyed by
  target platform: account-level failover already covers the accounts of a
  platform, and a second route to the same platform is ignored.
- Converting between gateway protocols mid-request (see above).


For JSON-body endpoints, the gateway rewrites the request `model` field to the
route's `upstream_model` before dispatch. For Gemini native paths such as
`/v1beta/models/{model}:generateContent`, the gateway resolves `{model}` and
the handler forwards the resolved upstream model.

Codex Alpha Search and Live requests use the `responses` route domain. Live
requests resolve the model from `session.model`, including multipart `session`
payloads, and apply the configured `upstream_model` before dispatch.
Codex model manifest requests reuse the existing OpenAI account selection and
failover path within the Composite group.

Responses WebSocket resolves the first `response.create` model in the
`responses` route domain (including `any` routes). Only OpenAI and Grok targets
are supported. The public model remains the admission and response identity;
the route's upstream model is applied before channel/account mapping. Subsequent
turns can repeat or omit the public model. Changing it requires reconnecting so
the gateway can resolve the route and select an account again.

## Built-In Detection

Composite routing detects common public model IDs and provider-prefixed IDs:

- `claude-*` and `anthropic/claude-*` route to Anthropic.
- `gemini-*` and `google/gemini-*` route to Gemini.
- `gpt-*`, `o*`, `codex-*`, `text-embedding-*`, `dall-e-*`, and
  `openai/*` route to OpenAI.
- `grok-*` and `xai/grok-*` route to Grok.
- `kimi-*`, `moonshot-*`, and `k3` route to Kimi.
- `glm-*` routes to Zhipu GLM.
- `deepseek-*` routes to DeepSeek.
- `minimax-*`, `abab*`, and `minimax/*` route to MiniMax.

Unknown or ambiguous model names fail closed with a client error instead of
guessing a provider.

## Admin Workflows

- Admins can create a group with platform `composite`.
- Admins can add, edit, delete, and preview composite model routes.
- Composite groups can copy accounts from concrete provider groups.
- Concrete provider accounts can be assigned directly to composite groups from
  account create/edit and bulk account workflows.
- Subscription payment plans can bind to a composite group when that group's
  `subscription_type` is `subscription`. The plan grants access to the
  composite group; each request is still billed and quota-checked against the
  resolved concrete provider platform.
- Channel configuration exposes composite groups in concrete provider sections.
  The channel `group_ids` payload is still flat; provider-specific model
  mapping and pricing remain keyed by concrete platform.

## Bucket 2 Setup: OpenAI + Claude + Gemini + Grok

Use one composite subscription group when one customer-facing plan should expose
model aliases across OpenAI, Claude, Gemini, and Grok without issuing separate
keys per provider.

1. Create concrete provider groups for the upstream account pools, for example
   `OpenAI Paid`, `Claude Paid`, `Gemini Paid`, and `Grok Paid`.
2. Create a `composite` group with `subscription_type` set to `subscription`.
3. Assign provider accounts directly to the composite group, or copy accounts
   from the concrete provider groups during group creation.
4. Add explicit routes for public aliases that should not rely on built-in
   model detection:

   | Public model | Endpoint | Target platform | Upstream model |
   | --- | --- | --- | --- |
   | `all/gpt-5` | `responses` | `openai` | `gpt-5` |
   | `all/claude-sonnet` | `messages` | `anthropic` | `claude-sonnet-4-6` |
   | `all/gemini-pro` | `gemini` | `gemini` | `gemini-2.5-pro` |
   | `all/grok` | `responses` | `grok` | `grok-4.3` |

5. Configure channel pricing and model mapping under the concrete platforms
   named in each route. Composite routing does not create pricing records.
6. Create a subscription payment plan for the composite group.

The same composite group can also rely on built-in detection for standard model
names such as `gpt-*`, `claude-*`, `gemini-*`, and `grok-*`. Explicit routes are
recommended for bundled plan aliases because they make endpoint, provider, and
upstream model attribution reviewable in the admin UI.

## Limits

Composite routes choose a concrete provider and upstream model; they do not
create synthetic model metadata, pricing, or upstream capability records by
themselves. Keep channel pricing/model mapping configured for the concrete
provider platforms that the routes target.

This PR intentionally does not implement:

- AUTO smart-routing among multiple providers for the same abstract task.
- Direct API-key binding to several existing groups without a composite group.
- Protocol-agnostic provider decoupling or a LiteLLM-style adapter rewrite.

Cross-platform priority failover (see above) is the narrow, opt-in exception:
it only applies when an admin creates several routes for the same public model
on different platforms of the same gateway family.
