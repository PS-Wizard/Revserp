package app

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestDefaultIsEverythingEnabled(t *testing.T) {
	features := allFeaturesEnabled()
	for _, feature := range []Feature{FeatureAutoCrawl, FeatureGSCConnector, FeatureAIChat} {
		if !features.Enabled(feature) {
			t.Errorf("feature %q defaulted to disabled", feature)
		}
	}
}

func TestEnabledReadsEachFeatureIndependently(t *testing.T) {
	tests := []struct {
		name        string
		features    OrgFeatures
		feature     Feature
		wantEnabled bool
	}{
		{"autocrawl off", featuresFromRow(false, true, true, 50, 2, canonicalAIReasoningEfforts), FeatureAutoCrawl, false},
		{"autocrawl off leaves gsc on", featuresFromRow(false, true, true, 50, 2, canonicalAIReasoningEfforts), FeatureGSCConnector, true},
		{"gsc off", featuresFromRow(true, false, true, 50, 2, canonicalAIReasoningEfforts), FeatureGSCConnector, false},
		{"gsc off leaves autocrawl on", featuresFromRow(true, false, true, 50, 2, canonicalAIReasoningEfforts), FeatureAutoCrawl, true},
		{"ai chat off", featuresFromRow(true, true, false, 50, 2, canonicalAIReasoningEfforts), FeatureAIChat, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.features.Enabled(test.feature); got != test.wantEnabled {
				t.Errorf("Enabled(%q) = %v, want %v", test.feature, got, test.wantEnabled)
			}
		})
	}
}

func TestUnknownFeatureFailsOpen(t *testing.T) {
	if !featuresFromRow(false, false, false, 50, 2, canonicalAIReasoningEfforts).Enabled(Feature("not_a_real_feature")) {
		t.Error("an unrecognized feature resolved to disabled")
	}
}

func TestValidateAIChatSettings(t *testing.T) {
	tests := []struct {
		name        string
		limit       int32
		efforts     []string
		wantEfforts []string
		wantErr     bool
	}{
		{"defaults in canonical order", 50, []string{"max", "none", "high"}, []string{"none", "high", "max"}, false},
		{"zero limit", 0, []string{"none"}, []string{"none"}, false},
		{"maximum limit", 1000000, []string{"low"}, []string{"low"}, false},
		{"negative limit", -1, []string{"none"}, nil, true},
		{"limit too high", 1000001, []string{"none"}, nil, true},
		{"empty efforts", 50, []string{}, nil, true},
		{"duplicate effort", 50, []string{"low", "low"}, nil, true},
		{"unsupported effort", 50, []string{"medium"}, nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			efforts, err := validateAIChatSettings(test.limit, 2, test.efforts)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAIChatSettings() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if !slices.Equal(efforts, test.wantEfforts) {
				t.Errorf("validateAIChatSettings() = %v, want %v", efforts, test.wantEfforts)
			}
		})
	}
}

func TestDefaultAIChatSettings(t *testing.T) {
	features := allFeaturesEnabled()
	if features.AIMonthlyMessageLimit != 50 {
		t.Fatalf("default monthly limit = %d, want 50", features.AIMonthlyMessageLimit)
	}
	if !slices.Equal(features.AIAllowedReasoningEfforts, []string{"none", "low", "high", "max"}) {
		t.Fatalf("default efforts = %v, want all canonical efforts", features.AIAllowedReasoningEfforts)
	}
}

func TestAIChatFeatureGateUsesStableError(t *testing.T) {
	app := &App{}
	resolver := func(*App, *http.Request) (OrgFeatures, error) {
		return featuresFromRow(true, true, false, 50, 2, canonicalAIReasoningEfforts), nil
	}
	response := httptest.NewRecorder()
	app.requireFeature(FeatureAIChat, resolver)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("disabled feature reached handler")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))

	if response.Code != http.StatusForbidden || response.Body.String() != "{\"error\":\"ai_chat_disabled\"}\n" {
		t.Fatalf("feature response = %d %q", response.Code, response.Body.String())
	}
}
