package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
	"github.com/ps-wizard/revserp/internal/runecms"
)

const runeTestEncryptionSecret = "rune-test-encryption-secret-not-a-real-secret"

func TestRuneConnectFailureMapping(t *testing.T) {
	cases := []struct {
		code       string
		status     int
		message    string
		underlying error
	}{
		{"invalid_endpoint", http.StatusBadRequest, "invalid rune endpoint", nil},
		{"invalid_token", http.StatusBadRequest, "invalid rune credentials", nil},
		{"unauthorized", http.StatusBadRequest, "invalid rune credentials", nil},
		{"unsupported_transport", http.StatusBadRequest, "unsupported rune endpoint", nil},
		{"unreachable", http.StatusBadGateway, "rune endpoint unreachable", nil},
		{"timeout", http.StatusGatewayTimeout, "rune endpoint timed out", nil},
		{"too_large", http.StatusBadGateway, "rune response too large", nil},
		{"invalid_tools", http.StatusBadGateway, "invalid rune tools response", nil},
		{"", http.StatusBadGateway, "failed to reach rune endpoint", nil},
		{"bogus", http.StatusBadGateway, "failed to reach rune endpoint", nil},
	}
	for _, tc := range cases {
		var err error = runeCodedError{code: tc.code}
		if tc.code == "" {
			// An error carrying secret-looking text must never be echoed.
			err = fmt.Errorf("dial https://rune.example/mcp with token committed-secret-xyz: %w", context.DeadlineExceeded)
			_ = tc.underlying
		}
		rec := httptest.NewRecorder()
		runeConnectFailure(rec, httptest.NewRequest(http.MethodPost, "/", nil), err)
		if rec.Code != tc.status {
			t.Errorf("code %q: status = %d, want %d", tc.code, rec.Code, tc.status)
		}
		var body map[string]string
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatalf("code %q: decode body: %v", tc.code, decodeErr)
		}
		if body["error"] != tc.message {
			t.Errorf("code %q: message = %q, want %q", tc.code, body["error"], tc.message)
		}
		if strings.Contains(rec.Body.String(), "committed-secret-xyz") {
			t.Errorf("code %q: response leaks remote error details", tc.code)
		}
	}
}

func TestNormalizeRuneToolsStoresOnlyPublicFields(t *testing.T) {
	raw, response, err := normalizeRuneTools([]RuneTool{
		{Name: "get_posts", Description: "List posts.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "get_post", Description: "", InputSchema: nil},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(response) != 2 || response[0].Name != "get_posts" || response[0].Description != "List posts." {
		t.Fatalf("response = %+v", response)
	}
	var stored []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("stored tools are not JSON: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored = %s", raw)
	}
	for _, tool := range stored {
		if len(tool) != 3 {
			t.Fatalf("stored tool has unexpected keys: %s", raw)
		}
		for _, key := range []string{"name", "description", "input_schema"} {
			if _, ok := tool[key]; !ok {
				t.Fatalf("stored tool missing %q: %s", key, raw)
			}
		}
	}
	if string(stored[1]["input_schema"]) != `{}` {
		t.Fatalf("empty schema normalized to %s, want {}", stored[1]["input_schema"])
	}
}

func TestNormalizeRuneToolsRejectsBadRemoteData(t *testing.T) {
	bad := [][]RuneTool{
		{{Name: "", InputSchema: json.RawMessage(`{}`)}},
		{
			{Name: "dup", InputSchema: json.RawMessage(`{}`)},
			{Name: "dup", InputSchema: json.RawMessage(`{}`)},
		},
		{{Name: "bad-schema", InputSchema: json.RawMessage(`{oops`)}},
		{{Name: strings.Repeat("n", runeMaxToolNameLen+1), InputSchema: json.RawMessage(`{}`)}},
		{{Name: strings.Repeat("g", runeMaxToolGroupLen+1), InputSchema: json.RawMessage(`{}`)}},
	}
	for i, tools := range bad {
		if _, _, err := normalizeRuneTools(tools); runeErrorCode(err) != "invalid_tools" {
			t.Errorf("case %d: err = %v, want invalid_tools code", i, err)
		}
	}
}

// TestNormalizeRuneToolsAcceptsLiveDiscoverySize is the regression for the
// reported connect failure: a live server advertising its real tool set must
// normalize, while the shared transport bound still rejects one entry more.
func TestNormalizeRuneToolsAcceptsLiveDiscoverySize(t *testing.T) {
	for _, count := range []int{0, 1, 89, 90, runecms.MaxDiscoveredTools} {
		tools := make([]RuneTool, 0, count)
		for i := range count {
			tools = append(tools, RuneTool{Name: fmt.Sprintf("tool_%d", i), Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)})
		}
		_, response, err := normalizeRuneTools(tools)
		if err != nil {
			t.Fatalf("%d tools: %v", count, err)
		}
		if len(response) != count {
			t.Errorf("%d tools: response has %d", count, len(response))
		}
	}
	over := make([]RuneTool, 0, runecms.MaxDiscoveredTools+1)
	for i := range runecms.MaxDiscoveredTools + 1 {
		over = append(over, RuneTool{Name: fmt.Sprintf("tool_%d", i), InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	if _, _, err := normalizeRuneTools(over); runeErrorCode(err) != "invalid_tools" {
		t.Errorf("oversized discovery accepted: %v", err)
	}
}

func TestReadStrictJSONRejectsUnknownFields(t *testing.T) {
	var body runeConnectRequest
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"endpoint_url":"https://rune.example/mcp","bearer_token":"t","extra":1}`))
	if readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
		t.Fatal("unknown field accepted")
	}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{oops`))
	if readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
		t.Fatal("malformed JSON accepted")
	}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"endpoint_url":"https://rune.example/mcp","bearer_token":"t"}`))
	if !readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
		t.Fatal("valid body rejected")
	}
}

// TestReadStrictJSONRejectsTrailingAndNull covers the bounded strict decoder:
// exactly one JSON value, null literals rejected (they decode silently into
// structs), and EOF required for non-optional bodies.
func TestReadStrictJSONRejectsTrailingAndNull(t *testing.T) {
	valid := `{"endpoint_url":"https://rune.example/mcp","bearer_token":"t"}`
	for _, payload := range []string{
		valid + ` {}`,
		valid + ` garbage`,
		`null`,
		`[]`,
		`"x"`,
		`123`,
		``,
		`   `,
	} {
		var body runeConnectRequest
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
		if readStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
			t.Errorf("payload %q accepted", payload)
		}
	}
}

// TestReadOptionalStrictJSONCoversCheckDisconnectContract covers the
// check/disconnect body contract: empty bodies allowed, but null,
// non-object, unknown-field, and trailing-JSON payloads rejected.
func TestReadOptionalStrictJSONCoversCheckDisconnectContract(t *testing.T) {
	for _, payload := range []string{``, `   `, `{}`} {
		var body struct{}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
		if !readOptionalStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
			t.Errorf("payload %q rejected, want accepted", payload)
		}
	}
	for _, payload := range []string{`null`, `[]`, `"x"`, `123`, `true`, `{"x":1}`, `{} {}`, `{oops`} {
		var body struct{}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
		if readOptionalStrictJSONOrRespond(httptest.NewRecorder(), req, &body) {
			t.Errorf("payload %q accepted, want rejected", payload)
		}
	}
}

func TestValidateRuneEndpoint(t *testing.T) {
	endpoint, ok := validateRuneEndpoint("  https://rune.example/mcp  ")
	if !ok || endpoint != "https://rune.example/mcp" {
		t.Fatalf("endpoint = %q, %v", endpoint, ok)
	}
	for _, value := range []string{"", "   ", "not a url", "https://", strings.Repeat("x", runeMaxEndpointLen)} {
		if _, ok := validateRuneEndpoint(value); ok {
			t.Errorf("endpoint %q accepted", value)
		}
	}
}

func TestNewRuneStatusResponseAlwaysArray(t *testing.T) {
	raw, err := json.Marshal(newRuneStatusResponse(false, "", time.Time{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tools":[]`) {
		t.Fatalf("disconnected body = %s, want empty tools array", raw)
	}
	for _, needle := range []string{"token", "cipher", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(string(raw)), needle) {
			t.Fatalf("status body mentions %q: %s", needle, raw)
		}
	}
	connected, err := json.Marshal(newRuneStatusResponse(true, "https://rune.example/mcp", time.Now(), nil))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(connected, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["connected"] != true || decoded["endpoint_url"] != "https://rune.example/mcp" {
		t.Fatalf("connected body = %s", connected)
	}
	if _, ok := decoded["last_checked_at"].(string); !ok {
		t.Fatalf("connected body missing last_checked_at: %s", connected)
	}
}

// newRuneTestPool connects to an explicit disposable/test database only. It
// never falls back to DATABASE_URL, so integration tests cannot mutate a
// development or production database.
func newRuneTestPool(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("RUNE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RUNE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("rune test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return sqlc.New(pool), pool, ctx
}

type runeFixture struct {
	app       *App
	queries   *sqlc.Queries
	pool      *pgxpool.Pool
	ctx       context.Context
	orgID     pgtype.UUID
	projectID pgtype.UUID
}

func newRuneFixture(t *testing.T, role string) runeFixture {
	t.Helper()
	queries, pool, ctx := newRuneTestPool(t)
	name := fmt.Sprintf("rune-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,$3)`, orgID, userID, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'rune-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	app := &App{
		DB:         pool,
		Queries:    queries,
		Config:     config.Config{GoogleTokenEncryptionSecret: runeTestEncryptionSecret},
		GSCService: gsc.NewService("", "", "", runeTestEncryptionSecret, 1<<20),
	}
	return runeFixture{app: app, queries: queries, pool: pool, ctx: ctx, orgID: orgID, projectID: projectID}
}

func (f runeFixture) userID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT user_id FROM organization_members WHERE org_id = $1 LIMIT 1`, f.orgID).Scan(&id); err != nil {
		t.Fatalf("fixture user: %v", err)
	}
	return id
}

type fakeRuneSession struct {
	tools  []RuneTool
	closed *bool
}

func (s *fakeRuneSession) Tools() []RuneTool { return s.tools }
func (s *fakeRuneSession) Close() error      { *s.closed = true; return nil }

func runeHandlerRequest(t *testing.T, method, projectID, body string, userID pgtype.UUID) *http.Request {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/projects/"+projectID, reader)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("projectID", projectID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	return req.WithContext(ctx)
}

func decodeRuneStatus(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rune response: %v (%s)", err, rec.Body.String())
	}
	return body
}

func assertNoTokenMaterial(t *testing.T, body string) {
	t.Helper()
	for _, needle := range []string{"bearer_token", "encrypted_token", "ciphertext", "test-token", "rune-secret-token"} {
		if strings.Contains(body, needle) {
			t.Fatalf("response contains token material %q: %s", needle, body)
		}
	}
}

func TestRuneStatusReportsSavedConnection(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	secret := "rune-secret-token-status"
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		closed := false
		return &fakeRuneSession{tools: []RuneTool{{Name: "get_posts", Description: "List posts.", InputSchema: json.RawMessage(`{"type":"object"}`)}}, closed: &closed}, nil
	}
	connectReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"`+secret+`"}`, userID)
	connectRec := httptest.NewRecorder()
	f.app.handleRuneConnect(connectRec, connectReq)
	if connectRec.Code != http.StatusOK {
		t.Fatalf("connect: status = %d (%s)", connectRec.Code, connectRec.Body.String())
	}

	statusReq := runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", userID)
	statusRec := httptest.NewRecorder()
	f.app.handleRuneStatus(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status: %d (%s)", statusRec.Code, statusRec.Body.String())
	}
	body := decodeRuneStatus(t, statusRec)
	if body["connected"] != true || body["endpoint_url"] != "https://rune.example/mcp" {
		t.Fatalf("status = %v", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("status tools = %v", body["tools"])
	}
	assertNoTokenMaterial(t, statusRec.Body.String())
}

func TestRuneStatusDisconnectedWhenNeverConnected(t *testing.T) {
	f := newRuneFixture(t, "member")
	userID := f.userID(t)
	req := runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", userID)
	rec := httptest.NewRecorder()
	f.app.handleRuneStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeRuneStatus(t, rec)
	if body["connected"] != false {
		t.Fatalf("status = %v", body)
	}
	if tools, ok := body["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("status tools = %v, want []", body["tools"])
	}
}

func TestRuneMutationsRequireOwner(t *testing.T) {
	f := newRuneFixture(t, "member")
	userID := f.userID(t)
	called := false
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		called = true
		closed := false
		return &fakeRuneSession{closed: &closed}, nil
	}
	connectReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"x"}`, userID)
	connectRec := httptest.NewRecorder()
	f.app.handleRuneConnect(connectRec, connectReq)
	if connectRec.Code != http.StatusForbidden {
		t.Fatalf("member connect: %d (%s)", connectRec.Code, connectRec.Body.String())
	}
	if called {
		t.Fatal("connector called for non-owner")
	}
	checkReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{}`, userID)
	checkRec := httptest.NewRecorder()
	f.app.handleRuneCheck(checkRec, checkReq)
	if checkRec.Code != http.StatusForbidden {
		t.Fatalf("member check: %d (%s)", checkRec.Code, checkRec.Body.String())
	}
	disconnectReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), "", userID)
	disconnectRec := httptest.NewRecorder()
	f.app.handleRuneDisconnect(disconnectRec, disconnectReq)
	if disconnectRec.Code != http.StatusForbidden {
		t.Fatalf("member disconnect: %d (%s)", disconnectRec.Code, disconnectRec.Body.String())
	}
}

func TestRuneConnectValidation(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	called := false
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		called = true
		closed := false
		return &fakeRuneSession{closed: &closed}, nil
	}
	for _, payload := range []string{
		`{"endpoint_url":"","bearer_token":"x"}`,
		`{"endpoint_url":"https://rune.example/mcp","bearer_token":""}`,
		`{"endpoint_url":"not a url","bearer_token":"x"}`,
		`{"endpoint_url":"https://rune.example/mcp","bearer_token":"x","unknown":1}`,
		`{oops`,
	} {
		req := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), payload, userID)
		rec := httptest.NewRecorder()
		f.app.handleRuneConnect(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %q: status = %d, want 400", payload, rec.Code)
		}
	}
	if called {
		t.Fatal("connector called for invalid payloads")
	}
}

func TestRuneConnectUnavailableWithoutClientOrSecret(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	payload := `{"endpoint_url":"https://rune.example/mcp","bearer_token":"x"}`
	f.app.RuneConnect = nil
	rec := httptest.NewRecorder()
	f.app.handleRuneConnect(rec, runeHandlerRequest(t, http.MethodPost, f.projectID.String(), payload, userID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil connector: %d, want 503", rec.Code)
	}
	closed := false
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		return &fakeRuneSession{closed: &closed}, nil
	}
	f.app.Config.GoogleTokenEncryptionSecret = ""
	rec = httptest.NewRecorder()
	f.app.handleRuneConnect(rec, runeHandlerRequest(t, http.MethodPost, f.projectID.String(), payload, userID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("empty secret: %d, want 500", rec.Code)
	}
}

func TestRuneConnectRevisionBumpsIncludingABA(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	var closed bool
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		if token != "rune-secret-token" {
			return nil, runeCodedError{code: "invalid_token"}
		}
		return &fakeRuneSession{
			tools:  []RuneTool{{Name: "get_posts", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			closed: &closed,
		}, nil
	}
	connect := func(endpoint string) map[string]any {
		t.Helper()
		closed = false
		req := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"`+endpoint+`","bearer_token":"rune-secret-token"}`, userID)
		rec := httptest.NewRecorder()
		f.app.handleRuneConnect(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("connect %s: %d (%s)", endpoint, rec.Code, rec.Body.String())
		}
		if !closed {
			t.Fatal("session was not closed before persist")
		}
		assertNoTokenMaterial(t, rec.Body.String())
		return decodeRuneStatus(t, rec)
	}
	connect("https://rune.example/mcp")
	first, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if !first.Revision.Valid {
		t.Fatal("revision is not set")
	}
	stored := string(first.Tools)
	if !strings.Contains(stored, `"get_posts"`) || strings.Contains(stored, "rune-secret-token") {
		t.Fatalf("stored tools = %s", stored)
	}
	decrypted, err := f.app.GSCService.DecryptSecret(first.EncryptedToken)
	if err != nil || decrypted != "rune-secret-token" {
		t.Fatalf("stored token does not round-trip: %v", err)
	}

	connect("https://rune2.example/mcp")
	second, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if second.Revision == first.Revision {
		t.Fatal("revision did not change on replacement")
	}

	// ABA: delete then reconnect must still mint a fresh revision.
	if _, err := f.queries.DeleteProjectCMSConnectionByProjectID(f.ctx, f.projectID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	connect("https://rune.example/mcp")
	third, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if third.Revision == first.Revision || third.Revision == second.Revision {
		t.Fatal("ABA reconnect reused a revision")
	}
}

func TestRuneConnectFailurePreservesOld(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	var closed bool
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		return &fakeRuneSession{
			tools:  []RuneTool{{Name: "get_posts", Description: "d", InputSchema: json.RawMessage(`{}`)}},
			closed: &closed,
		}, nil
	}
	seedReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"rune-secret-token"}`, userID)
	seedRec := httptest.NewRecorder()
	f.app.handleRuneConnect(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", seedRec.Code, seedRec.Body.String())
	}
	before, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}

	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		return nil, runeCodedError{code: "unreachable"}
	}
	failReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://other.example/mcp","bearer_token":"rune-secret-token"}`, userID)
	failRec := httptest.NewRecorder()
	f.app.handleRuneConnect(failRec, failReq)
	if failRec.Code != http.StatusBadGateway {
		t.Fatalf("failed connect: %d, want 502", failRec.Code)
	}
	if body := decodeRuneStatus(t, failRec); body["error"] != "rune endpoint unreachable" {
		t.Fatalf("failed connect body = %v", body)
	}
	after, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if after.EndpointUrl != before.EndpointUrl || after.Revision != before.Revision || !bytes.Equal(after.Tools, before.Tools) {
		t.Fatal("failed replacement mutated the stored connection")
	}
}

func TestRuneCheckRefreshesTools(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	var closed bool
	tools := []RuneTool{{Name: "get_posts", Description: "d", InputSchema: json.RawMessage(`{}`)}}
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		if endpoint != "https://rune.example/mcp" || token != "rune-secret-token" {
			return nil, runeCodedError{code: "unauthorized"}
		}
		return &fakeRuneSession{tools: tools, closed: &closed}, nil
	}
	seedReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"rune-secret-token"}`, userID)
	seedRec := httptest.NewRecorder()
	f.app.handleRuneConnect(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", seedRec.Code, seedRec.Body.String())
	}

	tools = append(tools, RuneTool{Name: "get_post", Description: "one", InputSchema: json.RawMessage(`{"type":"object"}`)})
	checkReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{}`, userID)
	checkRec := httptest.NewRecorder()
	f.app.handleRuneCheck(checkRec, checkReq)
	if checkRec.Code != http.StatusOK {
		t.Fatalf("check: %d (%s)", checkRec.Code, checkRec.Body.String())
	}
	body := decodeRuneStatus(t, checkRec)
	if names := toolNames(t, body); len(names) != 2 {
		t.Fatalf("check tools = %v", body["tools"])
	}
	assertNoTokenMaterial(t, checkRec.Body.String())

	// Empty body is also accepted for check.
	emptyReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), "", userID)
	emptyRec := httptest.NewRecorder()
	f.app.handleRuneCheck(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("empty-body check: %d (%s)", emptyRec.Code, emptyRec.Body.String())
	}
}

func toolNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	tools, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %v", body["tools"])
	}
	names := make([]string, 0, len(tools))
	for _, item := range tools {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tool = %v", item)
		}
		name, _ := entry["name"].(string)
		names = append(names, name)
		if _, hasDesc := entry["description"]; !hasDesc {
			t.Fatalf("tool missing description: %v", item)
		}
		if len(entry) != 2 {
			t.Fatalf("tool exposes extra fields: %v", item)
		}
	}
	return names
}

func TestRuneCheckConflictsOnConcurrentReplacement(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	var closed bool
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		// Simulate a concurrent replacement racing this check.
		if _, err := f.pool.Exec(ctx, `UPDATE project_cms_connections SET revision = gen_random_uuid() WHERE project_id = $1`, f.projectID); err != nil {
			return nil, err
		}
		return &fakeRuneSession{
			tools:  []RuneTool{{Name: "get_posts", Description: "d", InputSchema: json.RawMessage(`{}`)}},
			closed: &closed,
		}, nil
	}
	seed, err := f.queries.UpsertProjectCMSConnection(f.ctx, sqlc.UpsertProjectCMSConnectionParams{
		ProjectID:      f.projectID,
		Provider:       "rune",
		EndpointUrl:    "https://rune.example/mcp",
		EncryptedToken: mustEncryptRuneToken(t, f.app, "rune-secret-token"),
		Tools:          json.RawMessage(`[{"name":"get_posts","description":"d","input_schema":{}}]`),
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	checkReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{}`, userID)
	checkRec := httptest.NewRecorder()
	f.app.handleRuneCheck(checkRec, checkReq)
	if checkRec.Code != http.StatusConflict {
		t.Fatalf("conflicting check: %d (%s), want 409", checkRec.Code, checkRec.Body.String())
	}
	current, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if current.Revision == seed.Revision {
		t.Fatal("expected the concurrent replacement revision to win")
	}
}

func mustEncryptRuneToken(t *testing.T, app *App, token string) string {
	t.Helper()
	encrypted, err := app.GSCService.EncryptSecret(token)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return encrypted
}

func TestRuneDisconnectDeletes(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	var closed bool
	f.app.RuneConnect = func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		return &fakeRuneSession{
			tools:  []RuneTool{{Name: "get_posts", Description: "d", InputSchema: json.RawMessage(`{}`)}},
			closed: &closed,
		}, nil
	}
	seedReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), `{"endpoint_url":"https://rune.example/mcp","bearer_token":"rune-secret-token"}`, userID)
	seedRec := httptest.NewRecorder()
	f.app.handleRuneConnect(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed connect: %d (%s)", seedRec.Code, seedRec.Body.String())
	}
	for _, payload := range []string{`{oops`, `null`, `{"x":1}`, `{} {}`} {
		badRec := httptest.NewRecorder()
		f.app.handleRuneDisconnect(badRec, runeHandlerRequest(t, http.MethodPost, f.projectID.String(), payload, userID))
		if badRec.Code != http.StatusBadRequest {
			t.Fatalf("malformed disconnect %q: %d, want 400", payload, badRec.Code)
		}
	}
	if _, err := f.queries.GetProjectCMSConnectionByProjectID(f.ctx, f.projectID); err != nil {
		t.Fatalf("malformed disconnect deleted the connection: %v", err)
	}
	disconnectReq := runeHandlerRequest(t, http.MethodPost, f.projectID.String(), "", userID)
	disconnectRec := httptest.NewRecorder()
	f.app.handleRuneDisconnect(disconnectRec, disconnectReq)
	if disconnectRec.Code != http.StatusOK {
		t.Fatalf("disconnect: %d (%s)", disconnectRec.Code, disconnectRec.Body.String())
	}
	if body := decodeRuneStatus(t, disconnectRec); body["connected"] != false {
		t.Fatalf("disconnect body = %v", body)
	}
	statusReq := runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", userID)
	statusRec := httptest.NewRecorder()
	f.app.handleRuneStatus(statusRec, statusReq)
	if body := decodeRuneStatus(t, statusRec); body["connected"] != false {
		t.Fatalf("status after disconnect = %v", body)
	}
	againRec := httptest.NewRecorder()
	f.app.handleRuneDisconnect(againRec, runeHandlerRequest(t, http.MethodPost, f.projectID.String(), "", userID))
	if againRec.Code != http.StatusNotFound {
		t.Fatalf("second disconnect: %d, want 404", againRec.Code)
	}
}

func TestRuneRoutesRequireIntegrationsFeature(t *testing.T) {
	f := newRuneFixture(t, "owner")
	userID := f.userID(t)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO organization_features (org_id, integrations) VALUES ($1, FALSE)
		ON CONFLICT (org_id) DO UPDATE SET integrations = FALSE`, f.orgID); err != nil {
		t.Fatalf("disable integrations: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization_features WHERE org_id = $1`, f.orgID)
	})
	gated := f.app.requireFeature(FeatureIntegrations, featuresByProjectParam)
	next := gated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", userID)
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled integrations: %d, want 403", rec.Code)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE organization_features SET integrations = TRUE WHERE org_id = $1`, f.orgID); err != nil {
		t.Fatalf("enable integrations: %v", err)
	}
	rec = httptest.NewRecorder()
	next.ServeHTTP(rec, runeHandlerRequest(t, http.MethodGet, f.projectID.String(), "", userID))
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled integrations: %d, want 200", rec.Code)
	}
}
