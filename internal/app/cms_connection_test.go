package app

import (
	"strings"
	"testing"
	"time"
)

// TestCMSStatusResolvesCatalogueMetadataForRawNames covers the persisted
// discovered name spelling: discovery stores original names (list_content),
// while the catalogue is keyed by the namespaced name (wp__list_content). The
// status response must still resolve the local description, group and write
// flag for both spellings, and a server group always wins over the catalogue.
func TestCMSStatusResolvesCatalogueMetadataForRawNames(t *testing.T) {
	wp := func(tools []runeToolResponse) []cmsToolResponse {
		return newCMSStatusResponse(true, CMSProviderWordPress, "https://wp.example", time.Now(), tools).Tools
	}
	list := wp([]runeToolResponse{{Name: "list_content", Description: "Server text."}})
	if list[0].Group != "content" || !strings.Contains(list[0].Description, "List or filter posts") {
		t.Errorf("raw list_content = %+v, want the catalogue group and description", list[0])
	}
	// An explicit server group is dynamic metadata and must be preserved.
	grouped := wp([]runeToolResponse{{Name: "list_content", Description: "Server text.", Group: "custom"}})
	if grouped[0].Group != "custom" {
		t.Errorf("server group lost: %+v", grouped[0])
	}
	namespaced := wp([]runeToolResponse{{Name: "wp__list_content", Description: "Server text."}})
	if namespaced[0].Group != list[0].Group || namespaced[0].Description != list[0].Description {
		t.Errorf("namespaced entry = %+v, want the same metadata as %+v", namespaced[0], list[0])
	}
	for _, name := range []string{"update_content", "wp__update_content"} {
		tools := wp([]runeToolResponse{{Name: name, Description: "Server text."}})
		if !tools[0].Write {
			t.Errorf("%s write flag is false", name)
		}
	}
	unknown := wp([]runeToolResponse{{Name: "brand_new_thing", Description: "Server text.", Group: "media"}})
	if unknown[0].Name != "brand_new_thing" || unknown[0].Group != "media" || unknown[0].Write {
		t.Errorf("unknown entry = %+v, want the discovered metadata kept", unknown[0])
	}
}

// TestCMSWriteToolAcceptsRawAndNamespacedNames covers the status write flag for
// stored discovered names: the transport write sets are keyed by namespaced
// names, so raw Rune names must resolve too.
func TestCMSWriteToolAcceptsRawAndNamespacedNames(t *testing.T) {
	for name, want := range map[string]bool{
		"create_record":      true,
		"cms__create_record": true,
		"read_record":        false,
		"cms__read_record":   false,
	} {
		if got := cmsWriteTool(CMSProviderRune, name); got != want {
			t.Errorf("cmsWriteTool(rune, %q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"update_content":     true,
		"wp__update_content": true,
		"list_content":       false,
	} {
		if got := cmsWriteTool(CMSProviderWordPress, name); got != want {
			t.Errorf("cmsWriteTool(wordpress, %q) = %v, want %v", name, got, want)
		}
	}
}
