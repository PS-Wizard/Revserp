package app

import "time"

const googleOAuthStateTTL = 15 * time.Minute

type startProjectGSCConnectRequest struct {
	ReturnPath         string `json:"return_path"`
	Mode               string `json:"mode"`
	GoogleConnectionID string `json:"google_connection_id"`
}

type selectProjectGSCSiteRequest struct {
	SiteURL            string `json:"site_url"`
	GoogleConnectionID string `json:"google_connection_id"`
}

type projectGSCStatusResponse struct {
	HasGoogleConnection        bool                           `json:"has_google_connection"`
	GoogleConnectionID         string                         `json:"google_connection_id,omitempty"`
	GoogleAccountEmail         string                         `json:"google_account_email,omitempty"`
	GoogleStatus               string                         `json:"google_status,omitempty"`
	NeedsReconnect             bool                           `json:"needs_reconnect"`
	CanManageConnection        bool                           `json:"can_manage_connection"`
	Connected                  bool                           `json:"connected"`
	SelectedSite               *projectGSCSiteResponse        `json:"selected_site,omitempty"`
	AvailableSites             []projectGSCSiteResponse       `json:"available_sites"`
	TokenError                 string                         `json:"token_error,omitempty"`
	GoogleConnections          []projectGoogleAccountResponse `json:"google_connections,omitempty"`
	SelectedGoogleConnectionID string                         `json:"selected_google_connection_id,omitempty"`
}

// projectGoogleAccountResponse describes one org-owned Google account: the
// verified Google subject stays server-side, the email identifies the account.
type projectGoogleAccountResponse struct {
	ID                 string `json:"id"`
	GoogleAccountEmail string `json:"google_account_email,omitempty"`
	GoogleStatus       string `json:"google_status,omitempty"`
}

type projectGSCSiteResponse struct {
	SiteURL         string `json:"site_url"`
	PermissionLevel string `json:"permission_level,omitempty"`
	MatchScore      int    `json:"match_score,omitempty"`
}
