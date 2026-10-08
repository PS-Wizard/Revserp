// Package locationkeywords owns location keyword persistence, deterministic
// suggestions, and selected-to-Maps-draft sync in exactly one implementation.
// The HTTP handlers and the RevBot chat tools both call into this package:
// no caller keeps a second copy of the SQL or the normalization rules.
package locationkeywords

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

const (
	SourceUser     = "user"
	SourceSelected = "selected"
	// SourceRevserp holds saved Revbot Find-keywords suggestions, persisted
	// independently of the derived service/locality/landmark set.
	SourceRevserp = "revserp"
)

type KeywordGroup struct {
	Branded    []string `json:"branded"`
	NonBranded []string `json:"non_branded"`
}

type StoredKeyword struct {
	Keyword    string
	Normalized string
	Kind       string
	Source     string
}

type KeywordLists struct {
	UserDefined      KeywordGroup
	RevserpSuggested KeywordGroup
	Selected         KeywordGroup
}

type SuggestionKeys struct {
	Services   map[string]struct{}
	Localities map[string]struct{}
	Landmarks  map[string]pgtype.UUID
}

// ErrKeywordConflict marks one phrase filed under both branded and non_branded.
var ErrKeywordConflict = errors.New("location keyword conflict")

// NormalizeKeywordGroup validates one explicit branded/non_branded pair.
// Blanks drop out and the same phrase under both kinds conflicts. Location
// lists carry no per-kind count cap: any number of selected Maps keywords is
// allowed. Phrase length still validates.
func NormalizeKeywordGroup(branded, nonBranded []string) ([]string, []string, error) {
	brand, err := normalizeKeywordEntries(branded)
	if err != nil {
		return nil, nil, err
	}
	nonBrand, err := normalizeKeywordEntries(nonBranded)
	if err != nil {
		return nil, nil, err
	}
	brandKeys := make(map[string]struct{}, len(brand))
	for _, phrase := range brand {
		brandKeys[projectkeywords.NormalizeProjectKeywordKey(phrase)] = struct{}{}
	}
	for _, phrase := range nonBrand {
		if _, dup := brandKeys[projectkeywords.NormalizeProjectKeywordKey(phrase)]; dup {
			return nil, nil, errors.Join(ErrKeywordConflict, errors.New("phrase cannot be both branded and non_branded"))
		}
	}
	return brand, nonBrand, nil
}

func normalizeKeywordEntries(phrases []string) ([]string, error) {
	out := make([]string, 0, len(phrases))
	seen := make(map[string]struct{}, len(phrases))
	for _, phrase := range phrases {
		if projectkeywords.NormalizeProjectKeywordDisplay(phrase) == "" {
			continue
		}
		display, err := projectkeywords.ValidateProjectKeywordPhrase(phrase)
		if err != nil {
			return nil, err
		}
		key := projectkeywords.NormalizeProjectKeywordKey(display)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, display)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// BuildSuggestedKeywords derives deterministic suggestions from saved
// services, localities and landmark names only: bare ingredients plus every
// "{service} in {locality or landmark}" combination. No provider or model
// call, and product_description is never parsed here. A suggestion counts as
// branded only when it matches the location brand phrase.
func BuildSuggestedKeywords(services, localities, landmarkNames []string, brandName string) KeywordGroup {
	group := KeywordGroup{Branded: []string{}, NonBranded: []string{}}
	brandKey := projectkeywords.NormalizeProjectKeywordKey(brandName)
	seen := make(map[string]struct{})
	branded := make(map[string]string)
	nonBranded := make(map[string]string)
	consider := func(raw string) {
		display, err := projectkeywords.ValidateProjectKeywordPhrase(raw)
		if err != nil {
			return
		}
		key := projectkeywords.NormalizeProjectKeywordKey(display)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		if brandKey != "" && (key == brandKey) {
			branded[key] = display
			return
		}
		nonBranded[key] = display
	}
	for _, raw := range services {
		consider(raw)
	}
	for _, raw := range localities {
		consider(raw)
	}
	for _, raw := range landmarkNames {
		consider(raw)
	}
	places := append(append([]string{}, localities...), landmarkNames...)
	for _, service := range services {
		serviceDisplay := projectkeywords.NormalizeProjectKeywordDisplay(service)
		if serviceDisplay == "" {
			continue
		}
		for _, place := range places {
			placeDisplay := projectkeywords.NormalizeProjectKeywordDisplay(place)
			if placeDisplay == "" {
				continue
			}
			consider(serviceDisplay + " in " + placeDisplay)
		}
	}
	for _, display := range branded {
		group.Branded = append(group.Branded, display)
	}
	for _, display := range nonBranded {
		group.NonBranded = append(group.NonBranded, display)
	}
	sort.Strings(group.Branded)
	sort.Strings(group.NonBranded)
	return group
}

// OriginForKey attributes a selected phrase to the suggestion bucket it came
// from so the Maps draft keeps an honest origin. Matches are exact normalized
// keyword equality, never substring.
func OriginForKey(key string, keys SuggestionKeys) string {
	if _, ok := keys.Services[key]; ok {
		return "service"
	}
	if _, ok := keys.Localities[key]; ok {
		return "locality"
	}
	if _, ok := keys.Landmarks[key]; ok {
		return "landmark"
	}
	return "service"
}

// SuggestedOrigins maps one normalized suggested phrase to the input buckets it
// came from: "service", "locality" and/or "landmark". Reports label each
// suggestion with real provenance instead of a bare brand/non-brand guess.
type SuggestedOrigins map[string][]string

// SuggestedSourcesForPhrase classifies one generated suggestion by exact
// normalized match. A "{service} in {locality|landmark}" combination reports
// both buckets; an unknown phrase reports nothing rather than a guess.
func SuggestedSourcesForPhrase(phrase string, keys SuggestionKeys) []string {
	key := projectkeywords.NormalizeProjectKeywordKey(phrase)
	if key == "" {
		return nil
	}
	if _, ok := keys.Services[key]; ok {
		return []string{"service"}
	}
	if _, ok := keys.Localities[key]; ok {
		return []string{"locality"}
	}
	if _, ok := keys.Landmarks[key]; ok {
		return []string{"landmark"}
	}
	service, place, ok := strings.Cut(key, " in ")
	if !ok {
		return nil
	}
	service = strings.TrimSpace(service)
	place = strings.TrimSpace(place)
	if service == "" || place == "" {
		return nil
	}
	if _, ok := keys.Services[service]; !ok {
		return nil
	}
	if _, ok := keys.Localities[place]; ok {
		return []string{"service", "locality"}
	}
	if _, ok := keys.Landmarks[place]; ok {
		return []string{"service", "landmark"}
	}
	return nil
}

func SuggestedOriginsForGroup(group KeywordGroup, keys SuggestionKeys) SuggestedOrigins {
	out := SuggestedOrigins{}
	for _, phrase := range append(append([]string{}, group.Branded...), group.NonBranded...) {
		if sources := SuggestedSourcesForPhrase(phrase, keys); len(sources) > 0 {
			out[projectkeywords.NormalizeProjectKeywordKey(phrase)] = sources
		}
	}
	return out
}

// LandmarkIDForKey returns the landmark behind an exact normalized name
// match, or an invalid ID when the phrase names no landmark.
func LandmarkIDForKey(key string, keys SuggestionKeys) pgtype.UUID {
	if id, ok := keys.Landmarks[key]; ok {
		return id
	}
	return pgtype.UUID{}
}

// DB is the raw-SQL surface this package needs, satisfied by *pgxpool.Pool
// and pgx.Tx so reads run on the pool and writes run inside the caller's tx.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Queries is the sqlc surface this package needs, satisfied by *sqlc.Queries
// including transaction-scoped instances.
type Queries interface {
	ListLocationLandmarksForUser(ctx context.Context, arg sqlc.ListLocationLandmarksForUserParams) ([]sqlc.LocationLandmark, error)
	GetProjectBusinessProfileByProjectID(ctx context.Context, projectID pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error)
	LockProjectLocationQueriesForUser(ctx context.Context, arg sqlc.LockProjectLocationQueriesForUserParams) ([]sqlc.ProjectLocationQuery, error)
	InsertProjectLocationQueryForUser(ctx context.Context, arg sqlc.InsertProjectLocationQueryForUserParams) (sqlc.ProjectLocationQuery, error)
	UpdateProjectLocationQueryForUser(ctx context.Context, arg sqlc.UpdateProjectLocationQueryForUserParams) (sqlc.ProjectLocationQuery, error)
}

func LoadStoredKeywords(ctx context.Context, db DB, locationID pgtype.UUID) ([]StoredKeyword, error) {
	rows, err := db.Query(ctx, `SELECT keyword, normalized_keyword, kind, source FROM location_keywords WHERE location_id = $1 ORDER BY source, kind, normalized_keyword, id`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredKeyword
	for rows.Next() {
		var row StoredKeyword
		if err := rows.Scan(&row.Keyword, &row.Normalized, &row.Kind, &row.Source); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func GroupStoredKeywords(rows []StoredKeyword, source string) KeywordGroup {
	group := KeywordGroup{Branded: []string{}, NonBranded: []string{}}
	for _, row := range rows {
		if row.Source != source {
			continue
		}
		if row.Kind == projectkeywords.ProjectKeywordKindBrand {
			group.Branded = append(group.Branded, row.Keyword)
		} else {
			group.NonBranded = append(group.NonBranded, row.Keyword)
		}
	}
	return group
}

func SelectedKeywordTexts(rows []StoredKeyword) []string {
	var out []string
	for _, row := range rows {
		if row.Source == SourceSelected {
			out = append(out, row.Keyword)
		}
	}
	return out
}

func insertStoredKeywords(ctx context.Context, db DB, locationID pgtype.UUID, source, kind string, phrases []string) error {
	for _, phrase := range phrases {
		if _, err := db.Exec(ctx, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,$2,$3,$4,$5)`,
			locationID, phrase, projectkeywords.NormalizeProjectKeywordKey(phrase), kind, source); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceStoredKeywords atomically replaces both explicit lists. The caller
// owns the transaction; this never touches suggestions or Maps queries.
func ReplaceStoredKeywords(ctx context.Context, db DB, locationID pgtype.UUID, userBrand, userNonBrand, selectedBrand, selectedNonBrand []string) error {
	if _, err := db.Exec(ctx, `DELETE FROM location_keywords WHERE location_id = $1 AND source IN ('user','selected')`, locationID); err != nil {
		return err
	}
	if err := insertStoredKeywords(ctx, db, locationID, SourceUser, projectkeywords.ProjectKeywordKindBrand, userBrand); err != nil {
		return err
	}
	if err := insertStoredKeywords(ctx, db, locationID, SourceUser, projectkeywords.ProjectKeywordKindNonBrand, userNonBrand); err != nil {
		return err
	}
	if err := insertStoredKeywords(ctx, db, locationID, SourceSelected, projectkeywords.ProjectKeywordKindBrand, selectedBrand); err != nil {
		return err
	}
	return insertStoredKeywords(ctx, db, locationID, SourceSelected, projectkeywords.ProjectKeywordKindNonBrand, selectedNonBrand)
}

// ReplaceSelectedKeywords atomically replaces the selected list only,
// leaving the user list untouched.
func ReplaceSelectedKeywords(ctx context.Context, db DB, locationID pgtype.UUID, brand, nonBrand []string) error {
	if _, err := db.Exec(ctx, `DELETE FROM location_keywords WHERE location_id = $1 AND source = 'selected'`, locationID); err != nil {
		return err
	}
	if err := insertStoredKeywords(ctx, db, locationID, SourceSelected, projectkeywords.ProjectKeywordKindBrand, brand); err != nil {
		return err
	}
	return insertStoredKeywords(ctx, db, locationID, SourceSelected, projectkeywords.ProjectKeywordKindNonBrand, nonBrand)
}

// ReplaceRevserpKeywords atomically replaces the saved suggested list only,
// leaving the user and selected lists untouched. The caller owns the
// transaction and the owner check; this never touches Maps queries.
func ReplaceRevserpKeywords(ctx context.Context, db DB, locationID pgtype.UUID, brand, nonBrand []string) error {
	if _, err := db.Exec(ctx, `DELETE FROM location_keywords WHERE location_id = $1 AND source = 'revserp'`, locationID); err != nil {
		return err
	}
	if err := insertStoredKeywords(ctx, db, locationID, SourceRevserp, projectkeywords.ProjectKeywordKindBrand, brand); err != nil {
		return err
	}
	return insertStoredKeywords(ctx, db, locationID, SourceRevserp, projectkeywords.ProjectKeywordKindNonBrand, nonBrand)
}

// LoadSuggestedKeywords reads the saved suggestion inputs and unions them
// with the deterministic derived set. Services come from the independent
// location profile snapshot, localities from the location row, and every
// current landmark row suggests phrases regardless of selection: explicit
// selection lives in the selected keyword list. Saved revserp rows persist
// Revbot Find-keywords output; on an exact normalized collision the saved
// text and kind win over the mechanical derived entry.
func LoadSuggestedKeywords(ctx context.Context, db DB, queries Queries, projectID, locationID, userID pgtype.UUID, localitiesJSON []byte) (KeywordGroup, SuggestionKeys, error) {
	keys := SuggestionKeys{
		Services:   map[string]struct{}{},
		Localities: map[string]struct{}{},
		Landmarks:  map[string]pgtype.UUID{},
	}
	brandName, services, err := LocationProfileSnapshot(ctx, db, queries, projectID, locationID)
	if err != nil {
		return KeywordGroup{}, keys, err
	}
	for _, service := range services {
		keys.Services[projectkeywords.NormalizeProjectKeywordKey(service)] = struct{}{}
	}
	var localities []string
	if len(localitiesJSON) > 0 {
		if err := json.Unmarshal(localitiesJSON, &localities); err != nil {
			return KeywordGroup{}, keys, err
		}
	}
	for _, locality := range localities {
		keys.Localities[projectkeywords.NormalizeProjectKeywordKey(locality)] = struct{}{}
	}
	landmarkRows, err := queries.ListLocationLandmarksForUser(ctx, sqlc.ListLocationLandmarksForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     userID,
	})
	if err != nil {
		return KeywordGroup{}, keys, err
	}
	var landmarkNames []string
	for _, landmark := range landmarkRows {
		key := projectkeywords.NormalizeProjectKeywordKey(landmark.Name)
		if key == "" {
			continue
		}
		landmarkNames = append(landmarkNames, landmark.Name)
		if _, dup := keys.Landmarks[key]; !dup {
			keys.Landmarks[key] = landmark.ID
		}
	}
	stored, err := LoadStoredKeywords(ctx, db, locationID)
	if err != nil {
		return KeywordGroup{}, keys, err
	}
	derived := BuildSuggestedKeywords(services, localities, landmarkNames, brandName)
	return CombineSavedSuggestedKeywords(derived, GroupStoredKeywords(stored, SourceRevserp)), keys, nil
}

// CombineSavedSuggestedKeywords unions the deterministic derived set with
// saved revserp rows: the Find-keywords output a user explicitly saved.
// Matches are exact normalized keyword equality, never substring; on a
// collision the saved text and kind win. Both lists sort for determinism.
// Phrases the derived set cannot attribute keep no suggested origin rather
// than a guessed one.
func CombineSavedSuggestedKeywords(derived, saved KeywordGroup) KeywordGroup {
	out := KeywordGroup{Branded: []string{}, NonBranded: []string{}}
	seen := make(map[string]struct{})
	add := func(display, kind string) {
		key := projectkeywords.NormalizeProjectKeywordKey(display)
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		if kind == projectkeywords.ProjectKeywordKindBrand {
			out.Branded = append(out.Branded, display)
		} else {
			out.NonBranded = append(out.NonBranded, display)
		}
	}
	for _, display := range saved.Branded {
		add(display, projectkeywords.ProjectKeywordKindBrand)
	}
	for _, display := range saved.NonBranded {
		add(display, projectkeywords.ProjectKeywordKindNonBrand)
	}
	for _, display := range derived.Branded {
		add(display, projectkeywords.ProjectKeywordKindBrand)
	}
	for _, display := range derived.NonBranded {
		add(display, projectkeywords.ProjectKeywordKindNonBrand)
	}
	sort.Strings(out.Branded)
	sort.Strings(out.NonBranded)
	return out
}

// LocationProfileSnapshot reads the independent location profile copy: brand
// plus the services snapshot taken at copy time. Later parent service edits
// never propagate here. Without a location copy the brand falls back to the
// parent and services stay empty; live parent services are never read.
func LocationProfileSnapshot(ctx context.Context, db DB, queries Queries, projectID, locationID pgtype.UUID) (string, []string, error) {
	var brand string
	var rawServices []byte
	if err := db.QueryRow(ctx, `SELECT brand_name, services FROM location_business_profiles WHERE location_id = $1`, locationID).Scan(&brand, &rawServices); err == nil {
		services, err := businessprofile.DecodeStringSlice(rawServices)
		if err != nil {
			return "", nil, err
		}
		return brand, services, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	profile, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", []string{}, nil
		}
		return "", nil, err
	}
	return profile.BrandName, []string{}, nil
}

// SyncSelectedMapQueries makes the current map draft enabled flags exactly the
// normalized selected set: stored rows keep their text, ordinal, source,
// origin, and landmark linkage with only the enabled flag flipped, and missing
// selected phrases append as enabled manual rows after the highest ordinal.
// Deselected rows disable (never delete); reselected rows enable. Empty
// selection disables every map draft row. Frozen run snapshots, cells, and
// evidence live in other tables this function cannot reach: the Queries
// interface exposes draft rows only. Landmark origin and linkage come from
// exact normalized keyword matches only.
func SyncSelectedMapQueries(ctx context.Context, queries Queries, locationID, projectID, userID pgtype.UUID, selected []string, keys SuggestionKeys) error {
	wanted := make(map[string]struct{}, len(selected))
	for _, phrase := range selected {
		if key := projectkeywords.NormalizeProjectKeywordKey(phrase); key != "" {
			wanted[key] = struct{}{}
		}
	}
	existing, err := queries.LockProjectLocationQueriesForUser(ctx, sqlc.LockProjectLocationQueriesForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     userID,
	})
	if err != nil {
		return err
	}
	kept := make(map[string]struct{}, len(existing))
	var nextOrdinal int32
	for _, row := range existing {
		if row.Kind != "map" {
			continue
		}
		kept[row.Normalized] = struct{}{}
		if row.Ordinal >= nextOrdinal {
			nextOrdinal = row.Ordinal + 1
		}
		_, want := wanted[row.Normalized]
		if row.Enabled != want {
			if _, err := queries.UpdateProjectLocationQueryForUser(ctx, sqlc.UpdateProjectLocationQueryForUserParams{
				Text: row.Text, Normalized: row.Normalized, Ordinal: row.Ordinal, Enabled: want,
				Kind: row.Kind, Source: row.Source, Origin: row.Origin, LandmarkID: row.LandmarkID,
				ID: row.ID, LocationID: locationID, ProjectID: projectID, UserID: userID,
			}); err != nil {
				return err
			}
		}
	}
	if keys.Services == nil {
		keys.Services = map[string]struct{}{}
	}
	if keys.Localities == nil {
		keys.Localities = map[string]struct{}{}
	}
	if keys.Landmarks == nil {
		keys.Landmarks = map[string]pgtype.UUID{}
	}
	for _, phrase := range selected {
		key := projectkeywords.NormalizeProjectKeywordKey(phrase)
		if key == "" {
			continue
		}
		if _, dup := kept[key]; dup {
			continue
		}
		if len(phrase) > localvisibility.MaxMapQueryBytes {
			continue
		}
		origin := OriginForKey(key, keys)
		landmarkID := pgtype.UUID{}
		if origin == "landmark" {
			landmarkID = LandmarkIDForKey(key, keys)
		}
		if _, err := queries.InsertProjectLocationQueryForUser(ctx, sqlc.InsertProjectLocationQueryForUserParams{
			Text:       phrase,
			Normalized: key,
			Ordinal:    nextOrdinal,
			Enabled:    true,
			Kind:       "map",
			Source:     "manual",
			Origin:     origin,
			LandmarkID: landmarkID,
			LocationID: locationID,
			ProjectID:  projectID,
			UserID:     userID,
		}); err != nil {
			return err
		}
		kept[key] = struct{}{}
		nextOrdinal++
	}
	return nil
}
