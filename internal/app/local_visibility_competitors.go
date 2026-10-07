package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

type localVisibilityCompetitorResponse struct {
	PlaceID         string `json:"place_id"`
	Title           string `json:"title"`
	Address         string `json:"address"`
	QueryPointsSeen int    `json:"query_points_seen"`
	BestRank        *int   `json:"best_rank"`
	QueryIndexes    []int  `json:"query_indexes"`
	SameBrandDomain bool   `json:"same_brand_domain"`
}

type localVisibilityCompetitorsResponse struct {
	RunID                   string                              `json:"run_id"`
	PointIndex              *int                                `json:"point_index"`
	TargetPlaceID           string                              `json:"target_place_id"`
	Queries                 []string                            `json:"queries"`
	TotalQueryPoints        int                                 `json:"total_query_points"`
	ContributingQueryPoints int                                 `json:"contributing_query_points"`
	FailedQueryPoints       int                                 `json:"failed_query_points"`
	PendingQueryPoints      int                                 `json:"pending_query_points"`
	UnreadableQueryPoints   int                                 `json:"unreadable_query_points"`
	IdlessEntries           int                                 `json:"idless_entries"`
	Competitors             []localVisibilityCompetitorResponse `json:"competitors"`
}

type localVisibilityCompetitorWebsiteRawPlace struct {
	PlaceID *string         `json:"placeId"`
	Website json.RawMessage `json:"website"`
}

type localVisibilityCompetitorWebsiteRawResponse struct {
	Places *[]localVisibilityCompetitorWebsiteRawPlace `json:"places"`
}

type localVisibilityCompetitorAccumulator struct {
	title        string
	address      string
	bestRank     *int
	seen         map[[2]int]struct{}
	queryIndexes map[int]struct{}
}

var errLocalVisibilityPointNotInSnapshot = errors.New("local visibility competitors: point index not in frozen snapshot")

func buildLocalVisibilityCompetitors(runID string, snapshot localvisibility.LocalRunSnapshot, rows []sqlc.GetLocalVisibilityRunCompetitorResultsRow) (localVisibilityCompetitorsResponse, error) {
	return aggregateLocalVisibilityCompetitors(runID, snapshot, rows, nil)
}

func buildLocalVisibilityPointCompetitors(runID string, pointIndex int, snapshot localvisibility.LocalRunSnapshot, rows []sqlc.GetLocalVisibilityRunCompetitorResultsRow) (localVisibilityCompetitorsResponse, error) {
	inSnapshot := false
	for _, point := range snapshot.Points {
		if point.PointIndex == pointIndex {
			inSnapshot = true
			break
		}
	}
	if !inSnapshot {
		return localVisibilityCompetitorsResponse{}, errLocalVisibilityPointNotInSnapshot
	}
	scopeIndex := pointIndex
	return aggregateLocalVisibilityCompetitors(runID, snapshot, rows, &scopeIndex)
}

func aggregateLocalVisibilityCompetitors(runID string, snapshot localvisibility.LocalRunSnapshot, rows []sqlc.GetLocalVisibilityRunCompetitorResultsRow, scopeIndex *int) (localVisibilityCompetitorsResponse, error) {
	if snapshot.TargetPlaceID == "" {
		return localVisibilityCompetitorsResponse{}, errors.New("local visibility competitors: frozen target place id is missing")
	}
	rowsByCell := make(map[[2]int]sqlc.GetLocalVisibilityRunCompetitorResultsRow, len(rows))
	for _, row := range rows {
		rowsByCell[[2]int{int(row.PointIndex), int(row.QueryIndex)}] = row
	}
	points := append([]localvisibility.GridPoint(nil), snapshot.Points...)
	sort.Slice(points, func(i, j int) bool { return points[i].PointIndex < points[j].PointIndex })

	queries := snapshot.Queries
	if queries == nil {
		queries = []string{}
	}
	accumulators := make(map[string]*localVisibilityCompetitorAccumulator)
	targetHosts := make(map[string]struct{})
	competitorHosts := make(map[string]map[string]struct{})
	contributing, failed, pending, unreadable, idless := 0, 0, 0, 0, 0
	countedPoints := 0
	for queryIndex := range queries {
		for _, point := range points {
			key := [2]int{point.PointIndex, queryIndex}
			inScope := scopeIndex == nil || point.PointIndex == *scopeIndex
			if inScope {
				countedPoints++
			}
			row, ok := rowsByCell[key]
			if !ok {
				if inScope {
					pending++
				}
				continue
			}
			switch row.CallStatus {
			case "pending":
				if inScope {
					pending++
				}
			case "request_failed":
				if inScope {
					failed++
				}
			case "success_empty":
				if inScope {
					contributing++
				}
			case "success_nonempty":
				places, err := decodeLocalVisibilityPointPlaces(row.RawResponse, snapshot.TargetPlaceID)
				if err != nil || len(places) == 0 {
					if inScope {
						unreadable++
					}
					continue
				}
				if inScope {
					contributing++
				}
				for _, entry := range decodeLocalVisibilityCompetitorWebsiteHosts(row.RawResponse) {
					if entry.placeID == snapshot.TargetPlaceID {
						targetHosts[entry.host] = struct{}{}
						continue
					}
					if !inScope {
						continue
					}
					hosts := competitorHosts[entry.placeID]
					if hosts == nil {
						hosts = make(map[string]struct{})
						competitorHosts[entry.placeID] = hosts
					}
					hosts[entry.host] = struct{}{}
				}
				if !inScope {
					continue
				}
				for _, place := range places {
					if place.PlaceID == nil || *place.PlaceID == "" {
						idless++
						continue
					}
					placeID := *place.PlaceID
					if placeID == snapshot.TargetPlaceID {
						continue
					}
					accumulator, ok := accumulators[placeID]
					if !ok {
						accumulator = &localVisibilityCompetitorAccumulator{
							title:        place.Title,
							address:      place.Address,
							seen:         make(map[[2]int]struct{}),
							queryIndexes: make(map[int]struct{}),
						}
						accumulators[placeID] = accumulator
					}
					accumulator.seen[key] = struct{}{}
					accumulator.queryIndexes[queryIndex] = struct{}{}
					if place.Position != nil && *place.Position > 0 {
						if accumulator.bestRank == nil || *place.Position < *accumulator.bestRank {
							rank := *place.Position
							accumulator.bestRank = &rank
						}
					}
				}
			default:
				if inScope {
					pending++
				}
			}
		}
	}

	competitors := make([]localVisibilityCompetitorResponse, 0, len(accumulators))
	for placeID, accumulator := range accumulators {
		queryIndexes := make([]int, 0, len(accumulator.queryIndexes))
		for queryIndex := range accumulator.queryIndexes {
			queryIndexes = append(queryIndexes, queryIndex)
		}
		sort.Ints(queryIndexes)
		sameBrand := false
		for host := range competitorHosts[placeID] {
			if _, ok := targetHosts[host]; ok {
				sameBrand = true
				break
			}
		}
		competitors = append(competitors, localVisibilityCompetitorResponse{
			PlaceID:         placeID,
			Title:           accumulator.title,
			Address:         accumulator.address,
			QueryPointsSeen: len(accumulator.seen),
			BestRank:        accumulator.bestRank,
			QueryIndexes:    queryIndexes,
			SameBrandDomain: sameBrand,
		})
	}
	sort.Slice(competitors, func(i, j int) bool {
		if competitors[i].QueryPointsSeen != competitors[j].QueryPointsSeen {
			return competitors[i].QueryPointsSeen > competitors[j].QueryPointsSeen
		}
		if cmp := compareLocalVisibilityRanks(competitors[i].BestRank, competitors[j].BestRank); cmp != 0 {
			return cmp < 0
		}
		return competitors[i].PlaceID < competitors[j].PlaceID
	})

	return localVisibilityCompetitorsResponse{
		RunID:                   runID,
		PointIndex:              scopeIndex,
		TargetPlaceID:           snapshot.TargetPlaceID,
		Queries:                 queries,
		TotalQueryPoints:        countedPoints,
		ContributingQueryPoints: contributing,
		FailedQueryPoints:       failed,
		PendingQueryPoints:      pending,
		UnreadableQueryPoints:   unreadable,
		IdlessEntries:           idless,
		Competitors:             competitors,
	}, nil
}

func localVisibilityWebsiteHost(website string) (string, bool) {
	parsed, err := url.Parse(website)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", false
	}
	return host, true
}

type localVisibilityCompetitorWebsiteEntry struct {
	placeID string
	host    string
}

func decodeLocalVisibilityCompetitorWebsiteHosts(raw []byte) []localVisibilityCompetitorWebsiteEntry {
	var decoded localVisibilityCompetitorWebsiteRawResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Places == nil {
		return nil
	}
	entries := make([]localVisibilityCompetitorWebsiteEntry, 0, len(*decoded.Places))
	for _, place := range *decoded.Places {
		if place.PlaceID == nil || *place.PlaceID == "" {
			continue
		}
		var website string
		if len(place.Website) == 0 || json.Unmarshal(place.Website, &website) != nil {
			continue
		}
		host, ok := localVisibilityWebsiteHost(website)
		if !ok {
			continue
		}
		entries = append(entries, localVisibilityCompetitorWebsiteEntry{placeID: *place.PlaceID, host: host})
	}
	return entries
}

func compareLocalVisibilityRanks(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	default:
		return 0
	}
}

func (a *App) loadLocalVisibilityCompetitorRun(w http.ResponseWriter, r *http.Request) (string, localvisibility.LocalRunSnapshot, []sqlc.GetLocalVisibilityRunCompetitorResultsRow, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	runID, err := parseUUIDParam(chi.URLParam(r, "runID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid run id")
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	run, err := a.Queries.GetLocalVisibilityRunForUser(r.Context(), sqlc.GetLocalVisibilityRunForUserParams{
		ID:     runID,
		ID_2:   locationID,
		ID_3:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "local visibility run not found")
			return "", localvisibility.LocalRunSnapshot{}, nil, false
		}
		serverError(w, r, err)
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	var snapshot localvisibility.LocalRunSnapshot
	if err := json.Unmarshal(run.Snapshot, &snapshot); err != nil {
		serverError(w, r, err)
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	rows, err := a.Queries.GetLocalVisibilityRunCompetitorResults(r.Context(), run.ID)
	if err != nil {
		serverError(w, r, err)
		return "", localvisibility.LocalRunSnapshot{}, nil, false
	}
	return run.ID.String(), snapshot, rows, true
}

func (a *App) handleGetLocalVisibilityRunCompetitors(w http.ResponseWriter, r *http.Request) {
	runID, snapshot, rows, ok := a.loadLocalVisibilityCompetitorRun(w, r)
	if !ok {
		return
	}
	response, err := buildLocalVisibilityCompetitors(runID, snapshot, rows)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleGetLocalVisibilityRunPointCompetitors(w http.ResponseWriter, r *http.Request) {
	pointIndex, err := strconv.Atoi(chi.URLParam(r, "pointIndex"))
	if err != nil || pointIndex < 0 || pointIndex >= localvisibility.GridPointCount {
		writeJSONError(w, http.StatusBadRequest, "invalid point index")
		return
	}
	runID, snapshot, rows, ok := a.loadLocalVisibilityCompetitorRun(w, r)
	if !ok {
		return
	}
	response, err := buildLocalVisibilityPointCompetitors(runID, pointIndex, snapshot, rows)
	if err != nil {
		if errors.Is(err, errLocalVisibilityPointNotInSnapshot) {
			writeJSONError(w, http.StatusBadRequest, "invalid point index")
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
