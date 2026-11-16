package geminiapp

// Native protocol constants and model aliases.

const (
	// NativeOrigin is the Gemini App Robin frontend.
	NativeOrigin = "https://robinfrontend-pa.googleapis.com"
	// NativeRPC is the ProcessQuery RPC path.
	NativeRPC = "/$rpc/assistant.mobile.robin.RobinService/ProcessQuery"
	// NativeCompatibilityVersion is the client protocol snapshot sent upstream.
	NativeCompatibilityVersion = "1.135.1.1291"
	// NativeWireVersion is used by the standalone request-metadata builder.
	NativeWireVersion = "1.131.4.1198"
)

// Native model aliases. Alias -> native ModeCategory wire value.
const (
	ModelArgon     = "gemini-argon"
	ModelHydrogen  = "gemini-hydrogen"
	ModelFlashLite = "gemini-flash-lite"
)

// ModeCategory wire values.
const (
	ModeUltra     = 8 // Pro-tier reasoning and coding (gemini-argon)
	ModeFast      = 1 // Gemini 4 Hydrogen (gemini-hydrogen)
	ModeFlashLite = 6 // gemini-flash-lite
)

// Thinking levels accepted by the native API. Level 3 is asynchronous Deep
// Think and is rejected locally; level 4 is the highest synchronous level.
const (
	ThinkingNone     = 0
	ThinkingLow      = 1
	ThinkingExtended = 2
	ThinkingDeep     = 3
	ThinkingMax      = 4
)

// NativeModels lists supported aliases in display order.
var NativeModels = []string{ModelArgon, ModelHydrogen, ModelFlashLite}

// ModelModes maps aliases to native mode categories.
var ModelModes = map[string]int{
	ModelArgon:     ModeUltra,
	ModelHydrogen:  ModeFast,
	ModelFlashLite: ModeFlashLite,
}

// IsNativeModel reports whether alias is supported.
func IsNativeModel(alias string) bool {
	_, ok := ModelModes[alias]
	return ok
}

// ReasoningEffortLevel maps OpenAI reasoning_effort values to thinking levels.
// High/xhigh stay synchronous (2); level 3 is async Deep Think and excluded.
var ReasoningEffortLevel = map[string]int{
	"none":    ThinkingNone,
	"minimal": ThinkingNone,
	"low":     ThinkingLow,
	"medium":  ThinkingExtended,
	"high":    ThinkingExtended,
	"xhigh":   ThinkingExtended,
}
