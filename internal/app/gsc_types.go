package app

import "time"

const googleOAuthStateTTL = 15 * time.Minute

type startProjectGSCConnectRequest struct {
	ReturnPath string `json:"return_path"`
}

type selectProjectGSCSiteRequest struct {
	SiteURL string `json:"site_url"`
}

type projectGSCStatusResponse struct {
	HasGoogleConnection bool                     `json:"has_google_connection"`
	GoogleConnectionID  string                   `json:"google_connection_id,omitempty"`
	GoogleAccountEmail  string                   `json:"google_account_email,omitempty"`
	GoogleStatus        string                   `json:"google_status,omitempty"`
	NeedsReconnect      bool                     `json:"needs_reconnect"`
	CanManageConnection bool                     `json:"can_manage_connection"`
	Connected           bool                     `json:"connected"`
	SelectedSite        *projectGSCSiteResponse  `json:"selected_site,omitempty"`
	AvailableSites      []projectGSCSiteResponse `json:"available_sites"`
	TokenError          string                   `json:"token_error,omitempty"`
}

type projectGSCSiteResponse struct {
	SiteURL         string `json:"site_url"`
	PermissionLevel string `json:"permission_level,omitempty"`
	MatchScore      int    `json:"match_score,omitempty"`
}
