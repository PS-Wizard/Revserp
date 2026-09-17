package aichattools

import (
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestBusinessProfileReturnsNewFields(t *testing.T) {
	profile := makeProfile()
	profile.ProductDescription = text("Makes widgets and gadgets.")
	profile.TargetAudience = text("Small SaaS teams.")
	profile.BusinessCompetitors = []byte(`["CorpA","CorpB"]`)
	profile.BrandedKeywords = []byte(`["acme"]`)
	profile.NonBrandedKeywords = []byte(`["widgets","gadgets"]`)
	fake := &fakeBusinessProfileReader{profile: profile}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{}`))

	if response.ProductDescription != "Makes widgets and gadgets." {
		t.Fatalf("ProductDescription = %q", response.ProductDescription)
	}
	if response.TargetAudience != "Small SaaS teams." {
		t.Fatalf("TargetAudience = %q", response.TargetAudience)
	}
	if len(response.BusinessCompetitors) != 2 || response.BusinessCompetitors[0] != "CorpA" {
		t.Fatalf("BusinessCompetitors = %v", response.BusinessCompetitors)
	}
	if len(response.BrandedKeywords) != 1 || response.BrandedKeywords[0] != "acme" {
		t.Fatalf("BrandedKeywords = %v", response.BrandedKeywords)
	}
	if len(response.NonBrandedKeywords) != 2 {
		t.Fatalf("NonBrandedKeywords = %v", response.NonBrandedKeywords)
	}
}

func TestBusinessProfileNewFieldsEmptyNeverNull(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName:  "Acme",
		WebsiteUrl: "https://acme.example",
	}}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{}`))
	if response.BusinessCompetitors == nil || len(response.BusinessCompetitors) != 0 {
		t.Fatalf("BusinessCompetitors = %v, want empty non-nil", response.BusinessCompetitors)
	}
	if response.BrandedKeywords == nil || len(response.BrandedKeywords) != 0 {
		t.Fatalf("BrandedKeywords = %v, want empty non-nil", response.BrandedKeywords)
	}
	if response.NonBrandedKeywords == nil || len(response.NonBrandedKeywords) != 0 {
		t.Fatalf("NonBrandedKeywords = %v, want empty non-nil", response.NonBrandedKeywords)
	}
	if response.ProductDescription != "" || response.TargetAudience != "" {
		t.Fatalf("text fields = %q/%q, want empty", response.ProductDescription, response.TargetAudience)
	}
}

func TestBusinessProfileNewTextCaps(t *testing.T) {
	fake := &fakeBusinessProfileReader{profile: sqlc.GetProjectBusinessProfileByProjectIDForUserRow{
		BrandName:          "Acme",
		WebsiteUrl:         "https://acme.example",
		ProductDescription: text(strings.Repeat("p", 700)),
		TargetAudience:     text(strings.Repeat("a", 600)),
	}}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{}`))
	if want := strings.Repeat("p", 500) + "…"; response.ProductDescription != want {
		t.Fatalf("ProductDescription not capped at 500 with marker")
	}
	if want := strings.Repeat("a", 500) + "…"; response.TargetAudience != want {
		t.Fatalf("TargetAudience not capped at 500 with marker")
	}
}

func TestBusinessProfileCorruptNewListsFallBackEmpty(t *testing.T) {
	profile := makeProfile()
	profile.BusinessCompetitors = []byte(`not json`)
	profile.BrandedKeywords = []byte(`not json`)
	profile.NonBrandedKeywords = []byte(`not json`)
	profile.TargetKeywords = []byte(`["kept"]`)
	fake := &fakeBusinessProfileReader{profile: profile}
	response := decodeBusinessProfile(t, runBusinessProfile(t, fake, `{}`))
	if len(response.BusinessCompetitors) != 0 || len(response.BrandedKeywords) != 0 || len(response.NonBrandedKeywords) != 0 {
		t.Fatalf("corrupt lists should fall back to empty: %+v", response)
	}
	if len(response.TargetKeywords) != 1 {
		t.Fatalf("TargetKeywords = %v, want kept", response.TargetKeywords)
	}
}
