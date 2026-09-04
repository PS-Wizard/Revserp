package app

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// workspaceCrawls resolves and validates the comparable completed crawls. The
// current crawl is always explicit; the baseline defaults to the previous
// completed crawl in the same project.
func (a *App) workspaceCrawls(r *http.Request) (sqlc.GetCrawlByIDForUserRow, sqlc.GetCrawlByIDForUserRow, pgtype.UUID, error) {
	currentID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, err
	}
	var user sqlc.User
	if p, ok := principalFromContext(r.Context()); ok && p.User.ID.Valid {
		user = p.User
	} else if u, _, err := a.ensureCurrentUser(r, a.Queries); err == nil && u.ID.Valid {
		user = u
	} else {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, errors.New("missing principal")
	}
	current, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: currentID, UserID: user.ID})
	if err != nil {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, err
	}
	if current.Status != "completed" {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, errors.New("current crawl is not completed")
	}

	baselineID := currentID
	if raw := strings.TrimSpace(r.URL.Query().Get("baseline_crawl_id")); raw != "" {
		baselineID, err = parseUUIDParam(raw)
		if err != nil {
			return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, err
		}
	} else {
		baselineID, err = a.Queries.GetPreviousCompletedCrawlID(r.Context(), currentID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, err
			}
			baselineID = currentID
		}
	}
	baseline, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: baselineID, UserID: user.ID})
	if err != nil {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, err
	}
	if baseline.Status != "completed" || baseline.ProjectID != current.ProjectID {
		return sqlc.GetCrawlByIDForUserRow{}, sqlc.GetCrawlByIDForUserRow{}, pgtype.UUID{}, errors.New("crawls must be completed and belong to the same project")
	}
	return baseline, current, user.ID, nil
}

type workspaceDiffRow struct {
	URL             string
	Pillar          string
	Bucket          string
	IssueType       string
	Severity        string
	Message         string
	Details         string
	BaselineIssueID *string
	CurrentIssueID  *string
	ChangeType      string
	CurrentPageSeen bool
}

func (a *App) loadWorkspaceDiff(r *http.Request, baselineID, currentID, userID pgtype.UUID, url *string) ([]workspaceDiffRow, error) {
	q := a.Queries
	if q == nil {
		q = sqlc.New(a.DB)
	}
	rows, err := q.ListIssueWorkspaceDiff(r.Context(), sqlc.ListIssueWorkspaceDiffParams{
		BaselineID: baselineID,
		CurrentID:  currentID,
		UserID:     userID,
		UrlFilter:  valueOrEmpty(url),
	})
	if err != nil {
		return nil, err
	}
	var result []workspaceDiffRow
	for _, row := range rows {
		var baselineIDPtr *string
		if row.BaselineIssueID != "" {
			v := row.BaselineIssueID
			baselineIDPtr = &v
		}
		var currentIDPtr *string
		if row.CurrentIssueID != "" {
			v := row.CurrentIssueID
			currentIDPtr = &v
		}
		result = append(result, workspaceDiffRow{
			URL:             row.Url,
			Pillar:          row.Pillar,
			Bucket:          row.Bucket,
			IssueType:       row.IssueType,
			Severity:        row.Severity,
			Message:         row.Message,
			Details:         row.Details,
			BaselineIssueID: baselineIDPtr,
			CurrentIssueID:  currentIDPtr,
			ChangeType:      row.ChangeType,
			CurrentPageSeen: row.CurrentPageSeen,
		})
	}
	return result, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type workspaceWorkRow struct {
	WorkItemID          string
	AttemptID           string
	URL                 string
	SubjectKind         string
	Pillar              string
	Bucket              string
	IssueType           string
	Status              string
	VerificationCrawlID *string
	Contributors        []string
}

func (a *App) loadWorkspaceWork(r *http.Request, currentID pgtype.UUID, url *string) ([]workspaceWorkRow, error) {
	q := a.Queries
	if q == nil {
		q = sqlc.New(a.DB)
	}
	rows, err := q.ListIssueWorkspaceWork(r.Context(), sqlc.ListIssueWorkspaceWorkParams{
		CurrentID: currentID,
		UrlFilter: valueOrEmpty(url),
	})
	if err != nil {
		return nil, err
	}
	var result []workspaceWorkRow
	for _, row := range rows {
		var verificationID *string
		if row.VerificationCrawlID != "" {
			v := row.VerificationCrawlID
			verificationID = &v
		}
		result = append(result, workspaceWorkRow{
			WorkItemID:          row.WorkItemID,
			AttemptID:           row.AttemptID,
			URL:                 row.Url,
			SubjectKind:         row.SubjectKind,
			Pillar:              row.Pillar,
			Bucket:              row.Bucket,
			IssueType:           row.IssueType,
			Status:              row.Status,
			VerificationCrawlID: verificationID,
			Contributors:        row.Contributors,
		})
	}
	return result, nil
}

func workspaceIdentity(url, pillar, bucket, issueType string) string {
	return url + "\n" + pillar + "\n" + bucket + "\n" + issueType
}

func applyWorkspaceWork(diff []workspaceDiffRow, work []workspaceWorkRow, currentID string) []workspaceDiffRow {
	byIdentity := make(map[string]workspaceWorkRow, len(work))
	for _, item := range work {
		byIdentity[workspaceIdentity(item.URL, item.Pillar, item.Bucket, item.IssueType)] = item
	}
	for index := range diff {
		item, ok := byIdentity[workspaceIdentity(diff[index].URL, diff[index].Pillar, diff[index].Bucket, diff[index].IssueType)]
		if !ok || item.VerificationCrawlID == nil || *item.VerificationCrawlID != currentID {
			continue
		}
		if item.Status == "fixed" && diff[index].BaselineIssueID != nil {
			if item.SubjectKind == "group" && diff[index].CurrentIssueID != nil {
				newIssue := diff[index]
				newIssue.BaselineIssueID = nil
				newIssue.ChangeType = "new"
				diff = append(diff, newIssue)
				diff[index].CurrentIssueID = nil
			}
			if diff[index].CurrentIssueID == nil {
				diff[index].ChangeType = "fixed"
			}
		}
		if item.Status == "not_verified" && diff[index].BaselineIssueID != nil && diff[index].CurrentIssueID == nil {
			diff[index].ChangeType = "not_verified"
		}
	}
	return diff
}

func workspaceWorkItem(row workspaceWorkRow) map[string]any {
	return map[string]any{
		"work_item_id": row.WorkItemID, "attempt_id": row.AttemptID, "url": row.URL,
		"subject_kind": row.SubjectKind, "pillar": row.Pillar, "bucket": row.Bucket,
		"issue_type": row.IssueType, "status": row.Status,
		"verification_crawl_id": row.VerificationCrawlID, "contributors": row.Contributors,
	}
}

func workspaceIssue(row workspaceDiffRow) map[string]any {
	return map[string]any{
		"url": row.URL, "pillar": row.Pillar, "bucket": row.Bucket,
		"issue_type": row.IssueType, "severity": row.Severity,
		"message": row.Message, "details": row.Details,
		"baseline_issue_id": row.BaselineIssueID, "current_issue_id": row.CurrentIssueID,
		"change_type": row.ChangeType,
	}
}

func workspaceCrawlMeta(crawl sqlc.GetCrawlByIDForUserRow) map[string]any {
	return map[string]any{
		"id": crawl.ID.String(), "project_id": crawl.ProjectID.String(), "status": crawl.Status,
		"started_at": formatTimestamp(crawl.StartedAt), "completed_at": formatTimestamp(crawl.CompletedAt),
	}
}

func (a *App) handleGetIssueWorkspaceSummary(w http.ResponseWriter, r *http.Request) {
	baseline, current, userID, err := a.workspaceCrawls(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl or baseline not found")
			return
		}
		if strings.Contains(err.Error(), "not completed") || strings.Contains(err.Error(), "same project") {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, err := a.loadWorkspaceDiff(r, baseline.ID, current.ID, userID, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	work, err := a.loadWorkspaceWork(r, current.ID, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows = applyWorkspaceWork(rows, work, current.ID.String())

	counts := map[string]int{"fixed": 0, "new": 0, "still_open": 0, "not_verified": 0, "no_longer_detected": 0}
	workURLs := make(map[string]bool)
	workAttempts := make(map[string]string)
	workItems := make([]map[string]any, 0, len(work))
	for _, item := range work {
		workURLs[item.URL] = true
		workAttempts[item.AttemptID] = item.Status
		workItems = append(workItems, workspaceWorkItem(item))
	}
	workCounts := map[string]int{}
	for _, status := range workAttempts {
		workCounts[status]++
	}

	pages := map[string]map[string]any{}
	for _, row := range rows {
		counts[row.ChangeType]++
		if row.ChangeType == "still_open" && !workURLs[row.URL] {
			continue
		}
		page, ok := pages[row.URL]
		if !ok {
			page = map[string]any{"url": row.URL, "fixed_count": 0, "new_count": 0, "open_count": 0, "no_longer_detected_count": 0, "not_verified_count": 0}
			pages[row.URL] = page
		}
		switch row.ChangeType {
		case "fixed":
			page["fixed_count"] = page["fixed_count"].(int) + 1
		case "new":
			page["new_count"] = page["new_count"].(int) + 1
		case "still_open":
			page["open_count"] = page["open_count"].(int) + 1
		case "no_longer_detected":
			page["no_longer_detected_count"] = page["no_longer_detected_count"].(int) + 1
		case "not_verified":
			page["not_verified_count"] = page["not_verified_count"].(int) + 1
		}
	}
	for url := range workURLs {
		if _, ok := pages[url]; !ok {
			pages[url] = map[string]any{"url": url, "fixed_count": 0, "new_count": 0, "open_count": 0, "no_longer_detected_count": 0, "not_verified_count": 0}
		}
	}
	pageList := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		pageList = append(pageList, page)
	}
	sort.Slice(pageList, func(i, j int) bool { return pageList[i]["url"].(string) < pageList[j]["url"].(string) })
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"baseline_crawl": workspaceCrawlMeta(baseline), "current_crawl": workspaceCrawlMeta(current), "counts": counts, "work_counts": workCounts, "pages": pageList, "work_items": workItems})
}

func (a *App) handleListIssueWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
	baseline, current, userID, err := a.workspaceCrawls(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl or baseline not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := a.loadWorkspaceDiff(r, baseline.ID, current.ID, userID, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	work, err := a.loadWorkspaceWork(r, current.ID, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows = applyWorkspaceWork(rows, work, current.ID.String())
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "resolved" {
		status = "no_longer_detected"
	}
	switch status {
	case "", "all", "fixed", "new", "still_open", "awaiting_verification", "not_verified", "no_longer_detected":
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid workspace change status")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	items := make([]map[string]any, 0)
	if status == "awaiting_verification" {
		for _, item := range work {
			if item.Status == "awaiting_verification" || item.Status == "not_verified" {
				items = append(items, workspaceWorkItem(item))
			}
		}
	} else {
		for _, row := range rows {
			if status != "" && status != "all" && row.ChangeType != status {
				continue
			}
			items = append(items, workspaceIssue(row))
		}
	}
	total := len(items)
	start := minInt(int(offset), total)
	end := minInt(start+int(limit), total)
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"items": items[start:end], "pagination": paginationResponse{Limit: limit, Offset: offset, Count: int32(end - start), Total: int64(total)}})
}

func (a *App) handleSearchIssueWorkspacePages(w http.ResponseWriter, r *http.Request) {
	// Bound the input before any database access, matching the crawl page
	// search design: over 512 Unicode code points is rejected with 400.
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 512 {
		writeJSONError(w, http.StatusBadRequest, "query is too long")
		return
	}
	baseline, current, userID, err := a.workspaceCrawls(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl or baseline not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := `SELECT url, title, COUNT(*) OVER() FROM (SELECT url, MAX(title) AS title FROM (SELECT url, title FROM crawl_pages WHERE crawl_id=$1 UNION ALL SELECT url, title FROM crawl_pages WHERE crawl_id=$2) p WHERE $3='' OR strpos(lower(url), lower($3)) > 0 GROUP BY url) matches ORDER BY url LIMIT $4 OFFSET $5`
	rows, err := a.DB.Query(r.Context(), query, baseline.ID, current.ID, q, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	var total int64
	for rows.Next() {
		var url string
		var title *string
		if err := rows.Scan(&url, &title, &total); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		result = append(result, map[string]any{"url": url, "title": title})
	}
	if err := rows.Err(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	_ = userID
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"pages": result, "pagination": paginationResponse{Limit: limit, Offset: offset, Count: int32(len(result)), Total: total}})
}

func (a *App) handleGetIssueWorkspacePage(w http.ResponseWriter, r *http.Request) {
	baseline, current, userID, err := a.workspaceCrawls(r)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl or baseline not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	url := strings.TrimSpace(r.URL.Query().Get("url"))
	if url == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}
	rows, err := a.loadWorkspaceDiff(r, baseline.ID, current.ID, userID, &url)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	work, err := a.loadWorkspaceWork(r, current.ID, &url)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows = applyWorkspaceWork(rows, work, current.ID.String())
	issues := make([]map[string]any, 0)
	for _, row := range rows {
		issues = append(issues, workspaceIssue(row))
	}
	currentIssues := make([]map[string]any, 0)
	for _, row := range rows {
		if row.CurrentIssueID != nil {
			currentIssues = append(currentIssues, workspaceIssue(row))
		}
	}
	workItems := make([]map[string]any, 0, len(work))
	for _, item := range work {
		workItems = append(workItems, workspaceWorkItem(item))
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"page": map[string]any{"url": url, "current_crawl_id": current.ID.String()}, "issues": issues, "current_issues": currentIssues, "work_items": workItems})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
