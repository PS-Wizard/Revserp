package aeo

import (
	"encoding/json"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func hasAuthorSignal(pageFact shared.PageFact) bool {
	if strings.TrimSpace(pageFact.Author) != "" {
		return true
	}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		if strings.TrimSpace(ogTags["og:author"]) != "" || strings.TrimSpace(ogTags["article:author"]) != "" {
			return true
		}
	}
	return false
}

func hasPlainAuthorSignal(pageFact shared.PageFact) bool {
	if strings.TrimSpace(pageFact.Author) != "" {
		return true
	}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		return strings.TrimSpace(ogTags["og:author"]) != "" || strings.TrimSpace(ogTags["article:author"]) != ""
	}
	return false
}

func hasWeakAuthorSignal(pageFact shared.PageFact) bool {
	authorValues := []string{strings.TrimSpace(pageFact.Author)}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		authorValues = append(authorValues, strings.TrimSpace(ogTags["og:author"]), strings.TrimSpace(ogTags["article:author"]))
	}
	for _, authorValue := range authorValues {
		normalizedAuthorValue := strings.ToLower(strings.TrimSpace(authorValue))
		if normalizedAuthorValue == "" {
			continue
		}
		if _, isGenericAuthor := genericAuthorSignals[normalizedAuthorValue]; isGenericAuthor {
			return true
		}
	}
	return false
}

func authorSignalMatchesSchema(pageFact shared.PageFact) bool {
	authorNames := collectPlainAuthorNames(pageFact)
	if len(authorNames) == 0 {
		return false
	}
	schemaIdentityNames := collectSchemaIdentityNames(pageFact.JSONLD)
	if len(schemaIdentityNames) == 0 {
		return false
	}
	for authorName := range authorNames {
		if _, exists := schemaIdentityNames[authorName]; exists {
			return true
		}
	}
	return false
}

func collectPlainAuthorNames(pageFact shared.PageFact) map[string]struct{} {
	authorNames := make(map[string]struct{})
	addNormalizedName(authorNames, pageFact.Author)
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		addNormalizedName(authorNames, ogTags["og:author"])
		addNormalizedName(authorNames, ogTags["article:author"])
	}
	return authorNames
}

func collectSchemaIdentityNames(jsonLD []byte) map[string]struct{} {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return map[string]struct{}{}
	}
	identityNames := make(map[string]struct{})
	collectSchemaIdentityNamesInto(parsedJSONLD, identityNames)
	return identityNames
}

func collectSchemaIdentityNamesInto(value any, identityNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case map[string]any:
		for _, key := range []string{"author", "publisher"} {
			if nestedValue, exists := typedValue[key]; exists {
				collectSchemaNamesFromIdentityValue(nestedValue, identityNames)
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaIdentityNamesInto(nestedValue, identityNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaIdentityNamesInto(entry, identityNames)
		}
	}
}

func collectSchemaNamesFromIdentityValue(value any, identityNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case string:
		addNormalizedName(identityNames, typedValue)
	case map[string]any:
		if rawName, exists := typedValue["name"]; exists {
			if name, ok := rawName.(string); ok {
				addNormalizedName(identityNames, name)
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaNamesFromIdentityValue(nestedValue, identityNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaNamesFromIdentityValue(entry, identityNames)
		}
	}
}

func addNormalizedName(target map[string]struct{}, value string) {
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	if normalizedValue == "" {
		return
	}
	target[normalizedValue] = struct{}{}
}

var genericAuthorSignals = map[string]struct{}{
	"admin":  {},
	"team":   {},
	"staff":  {},
	"editor": {},
}
