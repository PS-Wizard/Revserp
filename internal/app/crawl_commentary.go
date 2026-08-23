package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type commentarySectionJSON struct {
	Summary         string   `json:"summary"`
	Strengths       []string `json:"strengths"`
	Concerns        []string `json:"concerns"`
	Recommendations []string `json:"recommendations"`
}

type commentaryJSON struct {
	Overall   commentarySectionJSON `json:"overall"`
	SEO       commentarySectionJSON `json:"seo"`
	AEO       commentarySectionJSON `json:"aeo"`
	PageSpeed commentarySectionJSON `json:"pagespeed"`
}

func (a *App) handleGetCrawlCommentary(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	ctx := r.Context()

	_, err = a.Queries.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}

	breakdown, err := a.Queries.GetCrawlScoreBreakdownByCrawlForUser(ctx, sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		serverError(w, r, err)
		return
	}

	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdown.BreakdownJson, &snapshot); err != nil {
		serverError(w, r, err)
		return
	}

	prompt := buildCommentaryPrompt(snapshot)

	content, model, err := a.generateAIText(ctx, prompt)
	if err != nil {
		serverError(w, r, err)
		return
	}

	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		if idx := strings.LastIndex(content, "```"); idx != -1 {
			content = content[:idx]
		}
		content = strings.TrimSpace(content)
	}

	var commentary commentaryJSON
	if err := json.Unmarshal([]byte(content), &commentary); err != nil {
		serverError(w, r, fmt.Errorf("ai commentary parse error: %w (raw: %s)", err, content))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"overall":   commentary.Overall,
		"seo":       commentary.SEO,
		"aeo":       commentary.AEO,
		"pagespeed": commentary.PageSpeed,
		"model":     model,
	})
}

func buildCommentaryPrompt(snapshot issueshared.ScoreBreakdownSnapshot) string {
	var b strings.Builder

	b.WriteString("You are a senior SEO strategist writing a professional audit report for a client.\n\n")
	b.WriteString("Crawl data:\n")
	fmt.Fprintf(&b, "- Overall: %d%%\n", snapshot.OverallScore)

	for _, pillar := range snapshot.Pillars {
		fmt.Fprintf(&b, "- %s: %d%% (weight %.0f%%)\n", pillar.Label, pillar.Score, pillar.Weight*100)
	}

	b.WriteString("\n")

	for _, pillar := range snapshot.Pillars {
		fmt.Fprintf(&b, "%s bucket breakdown:\n", pillar.Label)
		for _, bucket := range pillar.Buckets {
			fmt.Fprintf(&b, "  - %s: %d%% — %d affected URLs\n", bucket.Label, bucket.Score, bucket.AffectedURLCount)
		}
		b.WriteString("\n")
	}

	b.WriteString(`Return ONLY valid JSON (no markdown fences, no extra text) matching exactly this schema:
{
  "overall": {
    "summary": "2-3 sentence executive summary referencing specific numbers",
    "strengths": ["one sentence per strength, reference exact score and what it means for the site"],
    "concerns": ["one sentence per concern, reference exact score and affected URL count"],
    "recommendations": ["one actionable sentence per recommendation, name the specific bucket and what to do"]
  },
  "seo": { "summary": "...", "strengths": [], "concerns": [], "recommendations": [] },
  "aeo": { "summary": "...", "strengths": [], "concerns": [], "recommendations": [] },
  "pagespeed": { "summary": "...", "strengths": [], "concerns": [], "recommendations": [] }
}

Rules:
- strengths: only buckets >= 85%. If none, return [].
- concerns: only buckets < 75%. If none, return [].
- recommendations: 3-5 items, ordered by impact (lowest-scoring bucket first). Be specific — name the issue type, not just the bucket.
- No generic advice. Every sentence must reference a specific metric from the data above.
- Professional tone. Write as if presenting to a CMO.
`)

	return b.String()
}
