package aeo

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

func hasMeaningfulOGTags(ogTags []byte) bool {
	trimmedOGTags := bytes.TrimSpace(ogTags)
	if len(trimmedOGTags) == 0 || bytes.Equal(trimmedOGTags, []byte("null")) || bytes.Equal(trimmedOGTags, []byte("{}")) {
		return false
	}
	return true
}

func hasMeaningfulJSONLD(jsonLD []byte) bool {
	trimmedJSONLD := bytes.TrimSpace(jsonLD)
	if len(trimmedJSONLD) == 0 || bytes.Equal(trimmedJSONLD, []byte("null")) || bytes.Equal(trimmedJSONLD, []byte("[]")) {
		return false
	}
	return true
}

func hasArticleLikeJSONLDType(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasArticleLikeJSONLDTypeValue(parsedJSONLD)
}

func hasArticleLikeJSONLDTypeValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if rawGraphEntries, ok := typedValue["@graph"].([]any); ok {
			if slices.ContainsFunc(rawGraphEntries, hasArticleLikeJSONLDTypeValue) {
				return true
			}
		}
		return hasArticleLikeSchemaType(typedValue["@type"])
	case []any:
		if slices.ContainsFunc(typedValue, hasArticleLikeJSONLDTypeValue) {
			return true
		}
	}
	return false
}

func hasArticleLikeSchemaType(value any) bool {
	switch typedValue := value.(type) {
	case string:
		return isArticleLikeSchemaTypeName(typedValue)
	case []any:
		for _, entry := range typedValue {
			typeName, ok := entry.(string)
			if ok && isArticleLikeSchemaTypeName(typeName) {
				return true
			}
		}
	}
	return false
}

func isArticleLikeSchemaTypeName(typeName string) bool {
	switch strings.TrimSpace(typeName) {
	case "Article", "BlogPosting", "NewsArticle", "TechArticle":
		return true
	default:
		return false
	}
}

func hasOnlyGenericStructuredData(jsonLD []byte) bool {
	typeNames := collectSchemaTypeNames(jsonLD)
	if len(typeNames) == 0 {
		return false
	}
	for typeName := range typeNames {
		if _, isGenericType := genericSchemaTypes[typeName]; !isGenericType {
			return false
		}
	}
	return true
}

func hasSchemaType(jsonLD []byte, targetType string) bool {
	_, exists := collectSchemaTypeNames(jsonLD)[targetType]
	return exists
}

func hasAnySchemaType(jsonLD []byte, targetTypes []string) bool {
	typeNames := collectSchemaTypeNames(jsonLD)
	for _, targetType := range targetTypes {
		if _, exists := typeNames[targetType]; exists {
			return true
		}
	}
	return false
}

func hasSchemaCoreFields(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasSchemaCoreFieldsValue(parsedJSONLD)
}

func hasSchemaCoreFieldsValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if hasAnyNonEmptyField(typedValue, "name") && (hasAnyNonEmptyField(typedValue, "url") || hasAnyNonEmptyField(typedValue, "description")) {
			return true
		}
		for _, nestedValue := range typedValue {
			if hasSchemaCoreFieldsValue(nestedValue) {
				return true
			}
		}
	case []any:
		for _, entry := range typedValue {
			if hasSchemaCoreFieldsValue(entry) {
				return true
			}
		}
	}
	return false
}

func hasArticlePublisherIdentity(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasArticlePublisherIdentityValue(parsedJSONLD)
}

func hasArticlePublisherIdentityValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if hasArticleLikeSchemaType(typedValue["@type"]) && (hasAnyNonEmptyField(typedValue, "author") || hasAnyNonEmptyField(typedValue, "publisher") || hasAnyNonEmptyField(typedValue, "mainEntityOfPage")) {
			return true
		}
		for _, nestedValue := range typedValue {
			if hasArticlePublisherIdentityValue(nestedValue) {
				return true
			}
		}
	case []any:
		for _, entry := range typedValue {
			if hasArticlePublisherIdentityValue(entry) {
				return true
			}
		}
	}
	return false
}

func hasAnyNonEmptyField(value map[string]any, fieldName string) bool {
	rawValue, exists := value[fieldName]
	if !exists {
		return false
	}
	return valueHasContent(rawValue)
}

func valueHasContent(value any) bool {
	switch typedValue := value.(type) {
	case string:
		return strings.TrimSpace(typedValue) != ""
	case map[string]any:
		if rawName, exists := typedValue["name"]; exists {
			if name, ok := rawName.(string); ok && strings.TrimSpace(name) != "" {
				return true
			}
		}
		if rawURL, exists := typedValue["url"]; exists {
			if url, ok := rawURL.(string); ok && strings.TrimSpace(url) != "" {
				return true
			}
		}
		return len(typedValue) > 0
	case []any:
		for _, entry := range typedValue {
			if valueHasContent(entry) {
				return true
			}
		}
	}
	return false
}

func collectSchemaTypeNames(jsonLD []byte) map[string]struct{} {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return map[string]struct{}{}
	}
	typeNames := make(map[string]struct{})
	collectSchemaTypeNamesInto(parsedJSONLD, typeNames)
	return typeNames
}

func collectSchemaTypeNamesInto(value any, typeNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case map[string]any:
		if rawTypeValue, ok := typedValue["@type"]; ok {
			switch typeValue := rawTypeValue.(type) {
			case string:
				typeNames[strings.TrimSpace(typeValue)] = struct{}{}
			case []any:
				for _, entry := range typeValue {
					entryTypeName, ok := entry.(string)
					if ok {
						typeNames[strings.TrimSpace(entryTypeName)] = struct{}{}
					}
				}
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaTypeNamesInto(nestedValue, typeNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaTypeNamesInto(entry, typeNames)
		}
	}
}

var genericSchemaTypes = map[string]struct{}{
	"Thing":          {},
	"WebPage":        {},
	"CollectionPage": {},
	"ItemPage":       {},
}
