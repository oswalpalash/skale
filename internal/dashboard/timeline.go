package dashboard

import "time"

// Timeline is the dashboard contract for workload replica history.
type Timeline struct {
	Workload        string           `json:"workload"`
	GeneratedAt     time.Time        `json:"generatedAt"`
	WindowStart     time.Time        `json:"windowStart"`
	WindowEnd       time.Time        `json:"windowEnd"`
	Source          string           `json:"source"`
	Samples         []TimelineSample `json:"samples"`
	Demand          []SignalSample   `json:"demand,omitempty"`
	CPU             []SignalSample   `json:"cpu,omitempty"`
	Memory          []SignalSample   `json:"memory,omitempty"`
	Recommendation  *TimelinePoint   `json:"recommendation,omitempty"`
	UnavailableText string           `json:"unavailableText,omitempty"`
}

// TimelineSample is one historical replica observation.
type TimelineSample struct {
	Timestamp time.Time `json:"timestamp"`
	Current   float64   `json:"current"`
}

// SignalSample is one historical non-replica signal observation.
type SignalSample struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// TimelinePoint is a point-in-time recommendation overlay.
type TimelinePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Replicas  float64   `json:"replicas"`
	State     string    `json:"state,omitempty"`
}
