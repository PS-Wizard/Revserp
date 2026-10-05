# grant-local-visibility-access

Grants one existing backend user a `member` role in the organization that owns
the recorded local visibility fixture run. Use it to let a freshly signed-in
account open the already-imported recorded grid in the UI.

It is deliberately tiny and repeatable: one transaction inserts at most one
`organization_members` row. It does **not**

- call Serper or any other provider,
- reserve, spend, or refund Maps credits,
- create or copy organizations, projects, locations, runs, cells, or users,
- touch budgets, auth rows, or worker jobs.

## Prerequisites

- The local Postgres started with Podman, not Docker:

  ```bash
  cd revserp-backend
  podman-compose up -d
  podman-compose ps
  ```

- A local database named `local_seo_test` with all migrations applied. The
  migrator uses goose and records applied migrations in the standard
  `goose_db_version` table:

  ```bash
  cd revserp-backend
  DATABASE_URL='postgres://revserp:revserp@127.0.0.1:55439/local_seo_test?sslmode=disable' make migrate
  ```

  (`make migrate` is `go run ./cmd/migrate`; it must run from the backend root
  because migration paths are relative to the working directory.)

- The recorded fixture run `c8bdfb07-f8aa-44df-9d5f-32b617c189f3` already
  imported into that database. This command only grants access; it never
  imports or recreates the run.

- The target user already created by a real sign-in against this database.

## Steps

1. Start the backend against `local_seo_test` and sign the user in normally
   (`make api`, then complete the login flow in the frontend).

2. Look up the internal user id. This is the backend `users.id`, **not** the
   JWT `sub`:

   ```bash
   psql 'postgres://revserp:revserp@127.0.0.1:55439/local_seo_test?sslmode=disable' \
     -c "SELECT id, auth_provider, email, created_at FROM users ORDER BY created_at DESC;"
   ```

3. Run the command from `revserp-backend` with the explicit flags:

   ```bash
   go run ./cmd/grant-local-visibility-access \
     --database-url 'postgres://revserp:revserp@127.0.0.1:55439/local_seo_test?sslmode=disable' \
     --user-id '<users.id UUID from step 2>' \
     --allow-local-fixture-access
   ```

   Rerunning it is safe: it reports `membership already present` and changes
   nothing.

## Flags

| Flag | Required | Meaning |
|---|---|---|
| `--database-url` | yes | Explicit `postgres://` URL. Never read from `.env`. |
| `--user-id` | yes | Existing internal `users.id` UUID, not the JWT `sub`. |
| `--allow-local-fixture-access` | yes | Confirms the fixture organization is local and disposable. |

## Safety guards

- The database host must be `127.0.0.1`, `localhost`, or `::1`, and the
  database name must be exactly `local_seo_test`. The connection's
  `current_database()` is checked again after connecting.
- The user must exist. Test-only identities are refused: auth provider
  `local-seo-live` and emails ending in `@example.invalid`.
- Only `organization_members(org_id, user_id, role) VALUES (..., 'member')`
  with `ON CONFLICT (org_id, user_id) DO NOTHING` runs inside the transaction.
- The DSN is never printed or included in errors.

## Expected output

```text
grant-local-visibility-access: granted membership (role member, no other changes)
organization_id=d5f1281b-a451-40b6-9708-46c97f06fd43
project_id=8350e98a-60f2-49f5-be8e-91649566fcc1
location_id=d281e495-a8fc-43ca-ba67-1bb3ba599497
ui=/app/projects/8350e98a-60f2-49f5-be8e-91649566fcc1/locations/d281e495-a8fc-43ca-ba67-1bb3ba599497/grid
```

> **No new spend.** This command only inserts a membership row. It performs no
> provider calls and does not reserve, deduct, or re-spend any credits.

## Tests

```bash
cd revserp-backend
go test ./cmd/grant-local-visibility-access/
```

The tests cover flag, URL, and user validation only. They never open a
database or run the command.
