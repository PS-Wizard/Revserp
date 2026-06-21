package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

func (a *App) ensureFreshGoogleConnection(ctx context.Context, queries *sqlc.Queries, connection sqlc.GoogleConnection) (sqlc.GoogleConnection, string, error) {
	if connection.Status != "active" {
		return connection, "", &gsc.Error{Message: "google connection requires reconnect"}
	}

	accessToken, err := a.GSCService.DecryptSecret(textValue(connection.EncryptedAccessToken))
	if err != nil {
		return connection, "", err
	}
	if accessToken != "" && connection.AccessTokenExpiresAt.Valid && connection.AccessTokenExpiresAt.Time.UTC().After(time.Now().UTC().Add(time.Minute)) {
		return connection, accessToken, nil
	}

	refreshToken, err := a.GSCService.DecryptSecret(connection.EncryptedRefreshToken)
	if err != nil {
		return connection, "", err
	}
	refreshedToken, err := a.GSCService.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		markErr := queries.UpdateGoogleConnectionStatus(ctx, sqlc.UpdateGoogleConnectionStatusParams{
			ID:        connection.ID,
			Status:    "reauth_required",
			LastError: pgText(err.Error()),
		})
		if markErr == nil {
			connection.Status = "reauth_required"
			connection.LastError = pgText(err.Error())
		}
		return connection, "", err
	}

	encryptedAccessToken, err := a.GSCService.EncryptSecret(refreshedToken.AccessToken)
	if err != nil {
		return connection, "", err
	}
	encryptedRefreshToken := connection.EncryptedRefreshToken
	if strings.TrimSpace(refreshedToken.RefreshToken) != "" {
		encryptedRefreshToken, err = a.GSCService.EncryptSecret(refreshedToken.RefreshToken)
		if err != nil {
			return connection, "", err
		}
	}
	updatedConnection, err := queries.UpdateGoogleConnectionTokens(ctx, sqlc.UpdateGoogleConnectionTokensParams{
		ID:                    connection.ID,
		EncryptedAccessToken:  pgText(encryptedAccessToken),
		EncryptedRefreshToken: encryptedRefreshToken,
		AccessTokenExpiresAt:  timestamptzValue(computeGoogleTokenExpiry(refreshedToken.ExpiresIn)),
		Scope:                 coalesceString(refreshedToken.Scope, connection.Scope),
		Status:                "active",
		LastError:             pgtype.Text{},
	})
	if err != nil {
		return connection, "", err
	}
	return updatedConnection, refreshedToken.AccessToken, nil
}

func computeGoogleTokenExpiry(expiresInSeconds int) time.Time {
	expiresIn := expiresInSeconds
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	refreshSkewSeconds := expiresIn - 60
	if refreshSkewSeconds < 0 {
		refreshSkewSeconds = 0
	}
	return time.Now().UTC().Add(time.Duration(refreshSkewSeconds) * time.Second)
}

func coalesceString(primaryValue, fallbackValue string) string {
	if strings.TrimSpace(primaryValue) != "" {
		return primaryValue
	}
	return fallbackValue
}
