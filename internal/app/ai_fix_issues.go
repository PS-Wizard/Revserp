package app

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// sqlBuilder tracks SQL query arguments and auto-numbers positional parameters.
type sqlBuilder struct {
	buf  strings.Builder
	args []any
}

func (b *sqlBuilder) param(arg any) string {
	b.args = append(b.args, arg)
	return fmt.Sprintf("$%d", len(b.args))
}

// loadAIFixIssueRows loads a capped set of affected URL rows for the selected issue scope.
func loadAIFixIssueRows(r *http.Request, tx pgx.Tx, crawlID pgtype.UUID, userID pgtype.UUID, pillarID string, bucketIDs []string, issueTypeIDs []string, issueURLs []string) ([]aiFixIssueRow, error) {
	var b sqlBuilder
	b.buf.WriteString(`SELECT ci.url, ci.issue_type, ci.severity, ci.message, ci.details, cp.title, cp.meta_description, cp.h1
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
LEFT JOIN crawl_pages AS cp ON cp.id = ci.crawl_page_id
WHERE ci.crawl_id = `)
	b.buf.WriteString(b.param(crawlID))
	b.buf.WriteString(`
  AND om.user_id = `)
	b.buf.WriteString(b.param(userID))
	b.buf.WriteString(`
  AND ci.pillar = `)
	b.buf.WriteString(b.param(pillarID))
	b.buf.WriteString(`
  AND ci.bucket = ANY(`)
	b.buf.WriteString(b.param(bucketIDs))
	b.buf.WriteString(`)`)
	if len(issueTypeIDs) > 0 {
		b.buf.WriteString(`
  AND ci.issue_type = ANY(`)
		b.buf.WriteString(b.param(issueTypeIDs))
		b.buf.WriteString(`)`)
	}
	if len(issueURLs) > 0 {
		b.buf.WriteString(`
  AND ci.url = ANY(`)
		b.buf.WriteString(b.param(issueURLs))
		b.buf.WriteString(`)`)
	}
	b.buf.WriteString(`
ORDER BY ci.created_at ASC
LIMIT 40`)

	rows, err := tx.Query(r.Context(), b.buf.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	issueRows := make([]aiFixIssueRow, 0, maxAIFixContextIssueRows)
	for rows.Next() {
		var issueRow aiFixIssueRow
		var currentTitle pgtype.Text
		var currentDescription pgtype.Text
		var currentH1 pgtype.Text
		if err := rows.Scan(&issueRow.URL, &issueRow.IssueType, &issueRow.Severity, &issueRow.Message, &issueRow.Details, &currentTitle, &currentDescription, &currentH1); err != nil {
			return nil, err
		}
		issueRow.CurrentTitle = aiFixTextValue(currentTitle)
		issueRow.CurrentDescription = aiFixTextValue(currentDescription)
		issueRow.CurrentH1 = aiFixTextValue(currentH1)
		issueRows = append(issueRows, issueRow)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return issueRows, nil
}
