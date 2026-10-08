package app

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

var (
	errGoogleAccountNotFound    = errors.New("google account not found")
	errGoogleConnectionRequired = errors.New("google_connection_id is required")
	errGoogleAccountMismatch    = errors.New("google account does not match the reconnected account")
)

type googleAccountDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

const googleAccountConnectionColumns = `id, organization_id, connected_by_user_id,
	google_account_email, google_account_subject,
	encrypted_refresh_token, encrypted_access_token, access_token_expires_at,
	scope, status, last_error, created_at, updated_at`

func scanGoogleAccountConnection(row pgx.Row) (sqlc.GoogleConnection, error) {
	var connection sqlc.GoogleConnection
	err := row.Scan(
		&connection.ID,
		&connection.OrganizationID,
		&connection.ConnectedByUserID,
		&connection.GoogleAccountEmail,
		&connection.GoogleAccountSubject,
		&connection.EncryptedRefreshToken,
		&connection.EncryptedAccessToken,
		&connection.AccessTokenExpiresAt,
		&connection.Scope,
		&connection.Status,
		&connection.LastError,
		&connection.CreatedAt,
		&connection.UpdatedAt,
	)
	return connection, err
}

func listGoogleAccountConnectionsByOrganizationID(ctx context.Context, db googleAccountDB, organizationID pgtype.UUID) ([]sqlc.GoogleConnection, error) {
	rows, err := db.Query(ctx, `SELECT `+googleAccountConnectionColumns+` FROM google_connections WHERE organization_id = $1 ORDER BY created_at ASC, id ASC`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := []sqlc.GoogleConnection{}
	for rows.Next() {
		connection, err := scanGoogleAccountConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func getGoogleAccountConnectionByID(ctx context.Context, db googleAccountDB, id pgtype.UUID) (sqlc.GoogleConnection, bool, error) {
	connection, err := scanGoogleAccountConnection(db.QueryRow(ctx, `SELECT `+googleAccountConnectionColumns+` FROM google_connections WHERE id = $1 LIMIT 1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GoogleConnection{}, false, nil
		}
		return sqlc.GoogleConnection{}, false, err
	}
	return connection, true, nil
}

func getGoogleAccountConnectionByOrganizationSubject(ctx context.Context, db googleAccountDB, organizationID pgtype.UUID, subject string) (sqlc.GoogleConnection, bool, error) {
	connection, err := scanGoogleAccountConnection(db.QueryRow(ctx, `SELECT `+googleAccountConnectionColumns+` FROM google_connections WHERE organization_id = $1 AND google_account_subject = $2 LIMIT 1`, organizationID, pgText(subject)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GoogleConnection{}, false, nil
		}
		return sqlc.GoogleConnection{}, false, err
	}
	return connection, true, nil
}

type createGoogleAccountConnectionParams struct {
	OrganizationID        pgtype.UUID
	ConnectedByUserID     pgtype.UUID
	GoogleAccountEmail    string
	GoogleAccountSubject  string
	EncryptedRefreshToken string
	EncryptedAccessToken  string
	AccessTokenExpiresAt  pgtype.Timestamptz
	Scope                 string
}

func createGoogleAccountConnection(ctx context.Context, db googleAccountDB, params createGoogleAccountConnectionParams) (sqlc.GoogleConnection, error) {
	return scanGoogleAccountConnection(db.QueryRow(ctx, `INSERT INTO google_connections (
		organization_id, connected_by_user_id, google_account_email, google_account_subject,
		encrypted_refresh_token, encrypted_access_token, access_token_expires_at, scope, status, last_error
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', NULL)
	RETURNING `+googleAccountConnectionColumns,
		params.OrganizationID, params.ConnectedByUserID,
		pgText(params.GoogleAccountEmail), pgText(params.GoogleAccountSubject),
		params.EncryptedRefreshToken, pgText(params.EncryptedAccessToken),
		params.AccessTokenExpiresAt, params.Scope,
	))
}

func adoptGoogleAccountConnectionIdentity(ctx context.Context, db googleAccountDB, id pgtype.UUID, email, subject string) error {
	commandTag, err := db.Exec(ctx, `UPDATE google_connections
		SET google_account_email = $2, google_account_subject = $3, updated_at = now()
		WHERE id = $1`, id, pgText(email), pgText(subject))
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return errGoogleAccountNotFound
	}
	return nil
}

func revokeGoogleAccountConnection(ctx context.Context, db googleAccountDB, id pgtype.UUID, reason string) error {
	commandTag, err := db.Exec(ctx, `UPDATE google_connections
		SET status = 'revoked', encrypted_access_token = NULL, access_token_expires_at = NULL,
			last_error = $2, updated_at = now()
		WHERE id = $1`, id, pgText(reason))
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return errGoogleAccountNotFound
	}
	return nil
}

type googleOAuthStateWithPurpose struct {
	ID                 pgtype.UUID
	StateTokenHash     string
	OrganizationID     pgtype.UUID
	UserID             pgtype.UUID
	ProjectID          pgtype.UUID
	ReturnPath         string
	Purpose            string
	GoogleConnectionID pgtype.UUID
	ExpiresAt          pgtype.Timestamptz
}

func createGoogleOAuthStateWithPurpose(ctx context.Context, db googleAccountDB, stateTokenHash string, organizationID, userID, projectID pgtype.UUID, returnPath, purpose string, googleConnectionID pgtype.UUID, expiresAt pgtype.Timestamptz) (googleOAuthStateWithPurpose, error) {
	var state googleOAuthStateWithPurpose
	err := db.QueryRow(ctx, `INSERT INTO google_oauth_states (
		state_token_hash, organization_id, user_id, project_id, return_path, purpose, google_connection_id, expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	RETURNING id, state_token_hash, organization_id, user_id, project_id, return_path, purpose, google_connection_id, expires_at`,
		stateTokenHash, organizationID, userID, projectID, returnPath, purpose, uuidOrNull(googleConnectionID), expiresAt,
	).Scan(
		&state.ID, &state.StateTokenHash, &state.OrganizationID, &state.UserID,
		&state.ProjectID, &state.ReturnPath, &state.Purpose, &state.GoogleConnectionID, &state.ExpiresAt,
	)
	return state, err
}

func getGoogleOAuthStateWithPurpose(ctx context.Context, db googleAccountDB, stateTokenHash string) (googleOAuthStateWithPurpose, bool, error) {
	var state googleOAuthStateWithPurpose
	err := db.QueryRow(ctx, `SELECT id, state_token_hash, organization_id, user_id, project_id, return_path, purpose, google_connection_id, expires_at
		FROM google_oauth_states WHERE state_token_hash = $1 LIMIT 1`, stateTokenHash).Scan(
		&state.ID, &state.StateTokenHash, &state.OrganizationID, &state.UserID,
		&state.ProjectID, &state.ReturnPath, &state.Purpose, &state.GoogleConnectionID, &state.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return googleOAuthStateWithPurpose{}, false, nil
		}
		return googleOAuthStateWithPurpose{}, false, err
	}
	return state, true, nil
}

// matchGoogleAccountForSelect chooses which org-owned account backs a property
// selection. An explicit request wins, then the already-bound account; only a
// single-account org may fall back implicitly, never an arbitrary first row.
func matchGoogleAccountForSelect(accounts []sqlc.GoogleConnection, boundConnectionID *pgtype.UUID, requestedConnectionID *pgtype.UUID) (sqlc.GoogleConnection, error) {
	if requestedConnectionID != nil && requestedConnectionID.Valid {
		for _, account := range accounts {
			if uuidEqual(account.ID, *requestedConnectionID) {
				return account, nil
			}
		}
		return sqlc.GoogleConnection{}, errGoogleAccountNotFound
	}
	if boundConnectionID != nil && boundConnectionID.Valid {
		for _, account := range accounts {
			if uuidEqual(account.ID, *boundConnectionID) {
				return account, nil
			}
		}
		return sqlc.GoogleConnection{}, errGoogleAccountNotFound
	}
	if len(accounts) == 1 {
		return accounts[0], nil
	}
	if len(accounts) == 0 {
		return sqlc.GoogleConnection{}, errGoogleAccountNotFound
	}
	return sqlc.GoogleConnection{}, errGoogleConnectionRequired
}

// validateReconnectSubject rejects any reconnect where the established old subject
// differs from the freshly verified one. Empty never matches: an unverifiable
// old identity fails closed instead of adopting a new subject silently.
func validateReconnectSubject(oldSubject, verifiedSubject string) error {
	establishedSubject := strings.TrimSpace(oldSubject)
	verified := strings.TrimSpace(verifiedSubject)
	if establishedSubject == "" || verified == "" {
		return errGoogleAccountMismatch
	}
	if establishedSubject != verified {
		return errGoogleAccountMismatch
	}
	return nil
}

// matchGoogleAccountBySubject returns the account whose stored subject equals the
// verified subject. Legacy subject-less rows never match: they are preserved
// until the owner explicitly reconnects or reselects, so a new account can
// never overwrite an unknown old row's credentials or bindings.
func matchGoogleAccountBySubject(accounts []sqlc.GoogleConnection, subject string) (sqlc.GoogleConnection, bool) {
	wanted := strings.TrimSpace(subject)
	if wanted == "" {
		return sqlc.GoogleConnection{}, false
	}
	for _, account := range accounts {
		if strings.TrimSpace(textValue(account.GoogleAccountSubject)) == wanted {
			return account, true
		}
	}
	return sqlc.GoogleConnection{}, false
}

func uuidEqual(left, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.Bytes == right.Bytes
}

func uuidOrNull(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return id
}
