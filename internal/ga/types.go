// Package ga calls the Google Analytics Admin and Data APIs.
package ga

// Error reports a Google Analytics API failure.
type Error struct {
	StatusCode int
	Message    string
}

func (err *Error) Error() string { return err.Message }

// Property is one Analytics property available to a Google account.
type Property struct {
	PropertyID         string `json:"property_id"`
	DisplayName        string `json:"display_name"`
	AccountDisplayName string `json:"account_display_name,omitempty"`
}

// MetricPair compares current and preceding values.
type MetricPair struct {
	Current  float64 `json:"current"`
	Previous float64 `json:"previous"`
}

// Range is the date range used by an overview.
type Range struct {
	CurrentStart  string `json:"current_start"`
	CurrentEnd    string `json:"current_end"`
	PreviousStart string `json:"previous_start"`
	PreviousEnd   string `json:"previous_end"`
}

// Summary is the aggregate overview metrics.
type Summary struct {
	ActiveUsers    MetricPair `json:"active_users"`
	Sessions       MetricPair `json:"sessions"`
	EngagementRate MetricPair `json:"engagement_rate"`
	KeyEvents      MetricPair `json:"key_events"`
}

// TrendRow is one daily Analytics result.
type TrendRow struct {
	Date           string  `json:"date"`
	ActiveUsers    float64 `json:"active_users"`
	Sessions       float64 `json:"sessions"`
	EngagementRate float64 `json:"engagement_rate"`
	KeyEvents      float64 `json:"key_events"`
}

// BreakdownRow is one labeled Analytics breakdown result.
type BreakdownRow struct {
	Label          string  `json:"label"`
	ActiveUsers    float64 `json:"active_users"`
	Sessions       float64 `json:"sessions"`
	EngagementRate float64 `json:"engagement_rate"`
	KeyEvents      float64 `json:"key_events"`
}

// Overview is the Analytics data used by the project overview endpoint.
type Overview struct {
	HistoryDays  int            `json:"history_days"`
	WindowDays   int            `json:"window_days"`
	Range        Range          `json:"range"`
	Summary      Summary        `json:"summary"`
	Trend        []TrendRow     `json:"trend"`
	LandingPages []BreakdownRow `json:"landing_pages"`
	Channels     []BreakdownRow `json:"channels"`
	Sources      []BreakdownRow `json:"sources"`
	Countries    []BreakdownRow `json:"countries"`
	Devices      []BreakdownRow `json:"devices"`
}
