package geminiapp

import (
	"encoding/json"
	"strings"
)

// marshalJSON encodes value without HTML escaping so tool schemas and arguments
// survive the round trip verbatim.
func marshalJSON(value any) (string, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(builder.String(), "\n"), nil
}
