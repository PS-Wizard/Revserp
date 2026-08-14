package app

import "testing"

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
		{"autocrawl off", featuresFromRow(false, true, true), FeatureAutoCrawl, false},
		{"autocrawl off leaves gsc on", featuresFromRow(false, true, true), FeatureGSCConnector, true},
		{"gsc off", featuresFromRow(true, false, true), FeatureGSCConnector, false},
		{"gsc off leaves autocrawl on", featuresFromRow(true, false, true), FeatureAutoCrawl, true},
		{"ai chat off", featuresFromRow(true, true, false), FeatureAIChat, false},
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
	if !featuresFromRow(false, false, false).Enabled(Feature("not_a_real_feature")) {
		t.Error("an unrecognized feature resolved to disabled")
	}
}
