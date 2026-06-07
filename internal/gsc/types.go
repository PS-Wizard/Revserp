package gsc

const (
	googleAuthBaseURL             = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL                = "https://oauth2.googleapis.com/token"
	googleSitesURL                = "https://www.googleapis.com/webmasters/v3/sites"
	googleSearchAnalyticsURLBase  = "https://www.googleapis.com/webmasters/v3/sites"
	googleWebmastersReadOnlyScope = "https://www.googleapis.com/auth/webmasters.readonly"
)

var overviewWindowOptions = []int{180}

// Error reports one Google OAuth or Search Console failure.
type Error struct {
	Message string
}

func (err *Error) Error() string {
	return err.Message
}

// TokenResponse holds one Google OAuth token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// SiteEntry holds one accessible Search Console property.
type SiteEntry struct {
	SiteURL         string `json:"site_url"`
	PermissionLevel string `json:"permission_level,omitempty"`
	MatchScore      int    `json:"match_score,omitempty"`
}

// SearchAnalyticsRow holds one normalized Search Console row.
type SearchAnalyticsRow struct {
	Date        string  `json:"date,omitempty"`
	Query       string  `json:"query,omitempty"`
	Page        string  `json:"page,omitempty"`
	Country     string  `json:"country,omitempty"`
	Device      string  `json:"device,omitempty"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// OverviewRange holds one current/previous comparison window.
type OverviewRange struct {
	CurrentStart  string `json:"current_start"`
	CurrentEnd    string `json:"current_end"`
	PreviousStart string `json:"previous_start"`
	PreviousEnd   string `json:"previous_end"`
}

// MetricSummary holds one current/previous metric pair.
type MetricSummary struct {
	Current  float64 `json:"current"`
	Previous float64 `json:"previous"`
}

// OverviewSummary holds one aggregate metric set.
type OverviewSummary struct {
	Clicks      MetricSummary `json:"clicks"`
	Impressions MetricSummary `json:"impressions"`
	CTR         MetricSummary `json:"ctr"`
	Position    MetricSummary `json:"position"`
}

// OverviewOpportunities holds one small set of suggestion rows.
type OverviewOpportunities struct {
	LowCTRQueries           []SearchAnalyticsRow `json:"low_ctr_queries"`
	StrikingDistanceQueries []SearchAnalyticsRow `json:"striking_distance_queries"`
	QuestionQueries         []SearchAnalyticsRow `json:"question_queries"`
}

// OverviewWindow holds one Search Console overview window.
type OverviewWindow struct {
	Range            OverviewRange         `json:"range"`
	Summary          OverviewSummary       `json:"summary"`
	Trend            []SearchAnalyticsRow  `json:"trend"`
	TopQueries       []SearchAnalyticsRow  `json:"top_queries"`
	TopPages         []SearchAnalyticsRow  `json:"top_pages"`
	CountryBreakdown []SearchAnalyticsRow  `json:"country_breakdown"`
	DeviceBreakdown  []SearchAnalyticsRow  `json:"device_breakdown"`
	Opportunities    OverviewOpportunities `json:"opportunities"`
}

// OverviewPayload holds the Search Console overview payload returned to the app.
type OverviewPayload struct {
	HistoryDays int                       `json:"history_days"`
	Windows     map[string]OverviewWindow `json:"windows"`
}
