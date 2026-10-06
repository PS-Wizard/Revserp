package app

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/textnormalization"
)

// LandmarkQueryCandidate is one deterministic "{service} near {landmark}" map
// query draft, traceable to the saved landmark it was generated from.
type LandmarkQueryCandidate struct {
	Text       string
	LandmarkID pgtype.UUID
}

// GenerateLandmarkQueryCandidates returns one "{service} near {landmark}" draft
// per saved landmark and server-effective service, in landmark-then-service
// order. Services or landmarks may be empty. Duplicate normalized text is
// dropped, so two same-named landmarks yield one candidate rather than tripping
// the unique (location_id, kind, normalized) key. There is no result cap.
func GenerateLandmarkQueryCandidates(services []string, landmarks []sqlc.LocationLandmark) ([]LandmarkQueryCandidate, error) {
	labels, err := normalizeLandmarkQueryServices(services)
	if err != nil {
		return nil, err
	}
	candidates := make([]LandmarkQueryCandidate, 0, len(labels)*len(landmarks))
	seen := make(map[string]bool, len(labels)*len(landmarks))
	for _, landmark := range landmarks {
		name := textnormalization.NormalizeTextDisplay(landmark.Name)
		if name == "" {
			return nil, errors.New("generate landmark query candidates: landmark name must not be blank")
		}
		for _, label := range labels {
			text := textnormalization.NormalizeTextDisplay(label + " near " + name)
			if err := localvisibility.ValidateMapQueryCandidate(text); err != nil {
				return nil, err
			}
			key := textnormalization.NormalizeTextKey(text)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, LandmarkQueryCandidate{Text: text, LandmarkID: landmark.ID})
		}
	}
	return candidates, nil
}

func normalizeLandmarkQueryServices(services []string) ([]string, error) {
	out := make([]string, 0, len(services))
	seen := make(map[string]bool, len(services))
	for i, service := range services {
		if !utf8.ValidString(service) {
			return nil, fmt.Errorf("generate landmark query candidates: service %d is not valid UTF-8", i+1)
		}
		if strings.IndexByte(service, 0) >= 0 {
			return nil, fmt.Errorf("generate landmark query candidates: service %d contains a NUL byte", i+1)
		}
		display := textnormalization.NormalizeTextDisplay(service)
		if display == "" {
			continue
		}
		key := textnormalization.NormalizeTextKey(display)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, display)
	}
	return out, nil
}

// Migration 095 stored raw normalized keys, so dedup uses canonical keys from Text.
func planLandmarkQueryInserts(candidates []LandmarkQueryCandidate, existing []sqlc.ProjectLocationQuery, projectID, locationID, userID pgtype.UUID) []sqlc.InsertProjectLocationQueryForUserParams {
	seen := make(map[string]bool, len(existing)+len(candidates))
	next := int32(0)
	for _, row := range existing {
		if row.Kind != "map" {
			continue
		}
		seen[textnormalization.NormalizeTextKey(row.Text)] = true
		if row.Ordinal >= next {
			next = row.Ordinal + 1
		}
	}
	inserts := make([]sqlc.InsertProjectLocationQueryForUserParams, 0, len(candidates))
	for _, candidate := range candidates {
		key := textnormalization.NormalizeTextKey(candidate.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		inserts = append(inserts, sqlc.InsertProjectLocationQueryForUserParams{
			Text:       candidate.Text,
			Normalized: key,
			Ordinal:    next,
			Enabled:    false,
			Kind:       "map",
			Source:     "generated",
			Origin:     "landmark",
			LandmarkID: candidate.LandmarkID,
			LocationID: locationID,
			ProjectID:  projectID,
			UserID:     userID,
		})
		next++
	}
	return inserts
}
