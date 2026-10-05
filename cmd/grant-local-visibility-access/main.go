// Command grant-local-visibility-access gives one existing backend user a
// member role in the organization that owns the recorded local visibility
// fixture run.
//
// It is a narrow, repeatable opt-in: a single transaction inserts at most one
// organization_members row and changes nothing else. It never loads .env,
// never creates or copies organizations, projects, locations, runs, cells,
// users, or budgets, and never calls a provider.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// recordedFixtureRunID is the recorded live run already present in the
	// local UI database; this command only grants access to its organization.
	recordedFixtureRunID = "c8bdfb07-f8aa-44df-9d5f-32b617c189f3"
	// requiredLocalAccessDatabaseName is the only database this command writes.
	requiredLocalAccessDatabaseName = "local_seo_test"
	// recordedFixtureAuthProvider is the test-only recorder identity; users
	// with it are refused because they were never created by a real sign-in.
	recordedFixtureAuthProvider = "local-seo-live"
	// invalidFixtureEmailSuffix marks fixture-only emails; users with it are refused.
	invalidFixtureEmailSuffix = "@example.invalid"
	// grantedMembershipRole is the least-privileged organization membership.
	grantedMembershipRole = "member"
)

// grantAccessOptions holds the explicit CLI inputs. Nothing is read from the
// environment.
type grantAccessOptions struct {
	DatabaseURL             string
	UserID                  string
	AllowLocalFixtureAccess bool
}

// localAccessDatabaseTarget is the parsed, checked loopback database target.
type localAccessDatabaseTarget struct {
	Host     string
	Database string
}

// accessUser is the pre-existing backend user being granted access.
type accessUser struct {
	ID           pgtype.UUID
	AuthProvider string
	Email        string
}

// fixtureLocation is the organization, project, and location of the recorded run.
type fixtureLocation struct {
	OrganizationID pgtype.UUID
	ProjectID      pgtype.UUID
	LocationID     pgtype.UUID
}

// grantedAccess reports what the UI needs to open the recorded grid.
type grantedAccess struct {
	OrganizationID  string
	ProjectID       string
	LocationID      string
	MembershipAdded bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runGrantLocalVisibilityAccess(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "grant-local-visibility-access:", err)
		os.Exit(1)
	}
}

// runGrantLocalVisibilityAccess validates the flags, database target, and user
// id, then grants membership to the recorded fixture organization.
func runGrantLocalVisibilityAccess(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseGrantAccessFlags(args, stderr)
	if err != nil {
		return err
	}
	target, err := validateLocalAccessDatabaseURL(opts.DatabaseURL)
	if err != nil {
		return err
	}
	userID, err := parseAccessUserID(opts.UserID)
	if err != nil {
		return err
	}

	conn, err := pgx.Connect(ctx, opts.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to local UI database %s/%s: %w", target.Host, target.Database, err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	var currentDatabase string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&currentDatabase); err != nil {
		return fmt.Errorf("verify current database: %w", err)
	}
	if currentDatabase != requiredLocalAccessDatabaseName {
		return fmt.Errorf("connected to database %q, want %q", currentDatabase, requiredLocalAccessDatabaseName)
	}

	granted, err := grantRecordedFixtureAccessInTransaction(ctx, conn, userID)
	if err != nil {
		return err
	}
	printGrantedAccess(stdout, granted)
	return nil
}

// parseGrantAccessFlags parses and checks the required explicit flags.
func parseGrantAccessFlags(args []string, stderr io.Writer) (grantAccessOptions, error) {
	fs := flag.NewFlagSet("grant-local-visibility-access", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts grantAccessOptions
	fs.StringVar(&opts.DatabaseURL, "database-url", "", "explicit postgres:// URL of the loopback UI database; never read from .env")
	fs.StringVar(&opts.UserID, "user-id", "", "existing internal users.id (UUID), not the JWT sub")
	fs.BoolVar(&opts.AllowLocalFixtureAccess, "allow-local-fixture-access", false, "required safety flag confirming the fixture organization is local and disposable")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: grant-local-visibility-access --database-url URL --user-id UUID --allow-local-fixture-access")
		fmt.Fprintln(stderr, "Grants the user a member role in the recorded fixture organization. Inserts one membership row at most.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return grantAccessOptions{}, err
	}
	if opts.DatabaseURL == "" {
		return grantAccessOptions{}, errors.New("--database-url is required")
	}
	if opts.UserID == "" {
		return grantAccessOptions{}, errors.New("--user-id is required")
	}
	if !opts.AllowLocalFixtureAccess {
		return grantAccessOptions{}, errors.New("--allow-local-fixture-access is required: this changes membership in the local fixture organization")
	}
	return opts, nil
}

// validateLocalAccessDatabaseURL enforces the loopback host and database name
// guards. It accepts only postgres:// URLs so the database cannot be inferred.
func validateLocalAccessDatabaseURL(raw string) (localAccessDatabaseTarget, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return localAccessDatabaseTarget{}, errors.New("database url is not valid")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return localAccessDatabaseTarget{}, fmt.Errorf("database url must use the postgres:// scheme, got %q", parsed.Scheme)
	}
	connection, err := pgx.ParseConfig(raw)
	if err != nil {
		return localAccessDatabaseTarget{}, errors.New("database connection parameters are not valid")
	}
	hosts := []string{connection.Host}
	for _, fallback := range connection.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	for _, host := range hosts {
		switch strings.ToLower(host) {
		case "127.0.0.1", "localhost", "::1":
		default:
			return localAccessDatabaseTarget{}, fmt.Errorf("database host %q is not loopback", host)
		}
	}
	host := connection.Host
	database := connection.Database
	if database != requiredLocalAccessDatabaseName {
		return localAccessDatabaseTarget{}, fmt.Errorf("database name %q is not %q; refusing to change membership in a shared database", database, requiredLocalAccessDatabaseName)
	}
	return localAccessDatabaseTarget{Host: host, Database: database}, nil
}

// parseAccessUserID parses the internal users.id UUID argument.
func parseAccessUserID(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(raw)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("--user-id %q is not a UUID: %w", raw, err)
	}
	if !id.Valid {
		return pgtype.UUID{}, errors.New("--user-id is required")
	}
	return id, nil
}

// validateAccessUser refuses unknown, test-only, and fixture-only identities.
func validateAccessUser(user accessUser) error {
	if !user.ID.Valid {
		return errors.New("user id is missing")
	}
	if user.AuthProvider == recordedFixtureAuthProvider {
		return fmt.Errorf("user %s uses test-only auth provider %q; sign in with a real provider first", user.ID.String(), recordedFixtureAuthProvider)
	}
	if strings.TrimSpace(user.AuthProvider) == "" {
		return fmt.Errorf("user %s has no auth provider; refusing a test-only identity", user.ID.String())
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(user.Email)), invalidFixtureEmailSuffix) {
		return fmt.Errorf("user %s uses fixture-only email suffix %q; sign in with a real email first", user.ID.String(), invalidFixtureEmailSuffix)
	}
	return nil
}

// grantRecordedFixtureAccessInTransaction grants membership atomically and
// rolls back on any error, so a failed run changes nothing.
func grantRecordedFixtureAccessInTransaction(ctx context.Context, conn *pgx.Conn, userID pgtype.UUID) (grantedAccess, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return grantedAccess{}, fmt.Errorf("begin membership transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	granted, err := grantRecordedFixtureAccess(ctx, tx, userID)
	if err != nil {
		return grantedAccess{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return grantedAccess{}, fmt.Errorf("commit membership transaction: %w", err)
	}
	return granted, nil
}

// grantRecordedFixtureAccess looks up the recorded run, checks the user, and
// inserts at most one membership row.
func grantRecordedFixtureAccess(ctx context.Context, tx pgx.Tx, userID pgtype.UUID) (grantedAccess, error) {
	var runID pgtype.UUID
	if err := runID.Scan(recordedFixtureRunID); err != nil {
		return grantedAccess{}, fmt.Errorf("recorded fixture run id %q is not a UUID", recordedFixtureRunID)
	}

	location, err := findRecordedFixtureLocation(ctx, tx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return grantedAccess{}, fmt.Errorf("recorded run %s not found; import it into this database first", recordedFixtureRunID)
	}
	if err != nil {
		return grantedAccess{}, fmt.Errorf("look up recorded run: %w", err)
	}

	user, err := loadAccessUser(ctx, tx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return grantedAccess{}, fmt.Errorf("internal users.id %s does not exist; sign in to the UI backend against this database first", userID.String())
	}
	if err != nil {
		return grantedAccess{}, fmt.Errorf("load user: %w", err)
	}
	if err := validateAccessUser(user); err != nil {
		return grantedAccess{}, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO organization_members(org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO NOTHING`,
		location.OrganizationID, userID, grantedMembershipRole,
	)
	if err != nil {
		return grantedAccess{}, fmt.Errorf("insert organization membership: %w", err)
	}
	return grantedAccess{
		OrganizationID:  location.OrganizationID.String(),
		ProjectID:       location.ProjectID.String(),
		LocationID:      location.LocationID.String(),
		MembershipAdded: tag.RowsAffected() > 0,
	}, nil
}

// findRecordedFixtureLocation joins the recorded run to its organization,
// project, and location.
func findRecordedFixtureLocation(ctx context.Context, tx pgx.Tx, runID pgtype.UUID) (fixtureLocation, error) {
	var location fixtureLocation
	err := tx.QueryRow(ctx, `
		SELECT p.organization_id, l.project_id, l.id
		FROM local_visibility_runs r
		JOIN project_locations l ON l.id = r.location_id
		JOIN projects p ON p.id = l.project_id
		WHERE r.id = $1`, runID,
	).Scan(&location.OrganizationID, &location.ProjectID, &location.LocationID)
	return location, err
}

// loadAccessUser reads the existing backend user row.
func loadAccessUser(ctx context.Context, tx pgx.Tx, userID pgtype.UUID) (accessUser, error) {
	var user accessUser
	err := tx.QueryRow(ctx,
		`SELECT id, auth_provider, email FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.AuthProvider, &user.Email)
	return user, err
}

// printGrantedAccess prints the IDs and grid path, never the DSN.
func printGrantedAccess(w io.Writer, granted grantedAccess) {
	state := "granted membership"
	if !granted.MembershipAdded {
		state = "membership already present"
	}
	fmt.Fprintf(w, "grant-local-visibility-access: %s (role %s, no other changes)\n", state, grantedMembershipRole)
	fmt.Fprintf(w, "organization_id=%s\n", granted.OrganizationID)
	fmt.Fprintf(w, "project_id=%s\n", granted.ProjectID)
	fmt.Fprintf(w, "location_id=%s\n", granted.LocationID)
	fmt.Fprintf(w, "ui=/app/projects/%s/locations/%s/grid\n", granted.ProjectID, granted.LocationID)
}
