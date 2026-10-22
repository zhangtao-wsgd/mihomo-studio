package domain

import "time"

type Subscription struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Enabled     bool       `json:"enabled"`
	LastFetchAt *time.Time `json:"lastFetchAt,omitempty"`
	LastStatus  string     `json:"lastStatus"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type Terminal struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	IdentityType  string    `json:"identityType"`
	IdentityValue string    `json:"identityValue"`
	ProxyGroupID  string    `json:"proxyGroupId"`
	Enabled       bool      `json:"enabled"`
	Notes         string    `json:"notes"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ProxyGroup struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	MihomoType      string         `json:"mihomoType"`
	SelectionMode   string         `json:"selectionMode"`
	MemberNodeIDs   []string       `json:"memberNodeIds"`
	MemberNodeNames []string       `json:"memberNodeNames"`
	MemberFilter    map[string]any `json:"memberFilter"`
	HealthURL       string         `json:"healthUrl"`
	IntervalSeconds int            `json:"intervalSeconds"`
	ToleranceMS     int            `json:"toleranceMs"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RuleType  string    `json:"ruleType"`
	RuleValue string    `json:"ruleValue"`
	Target    string    `json:"target"`
	Priority  int       `json:"priority"`
	Source    string    `json:"source"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ConfigCandidate struct {
	ID                string    `json:"id"`
	SHA256            string    `json:"sha256"`
	SnapshotPath      string    `json:"snapshotPath"`
	Status            string    `json:"status"`
	ValidationMessage string    `json:"validationMessage"`
	SourceFingerprint string    `json:"sourceFingerprint"`
	CreatedAt         time.Time `json:"createdAt"`
}

type Deployment struct {
	ID                  string     `json:"id"`
	Operation           string     `json:"operation"`
	CandidateID         string     `json:"candidateId"`
	PreviousCandidateID string     `json:"previousCandidateId,omitempty"`
	Status              string     `json:"status"`
	RuntimeMessage      string     `json:"runtimeMessage"`
	StartedAt           time.Time  `json:"startedAt"`
	FinishedAt          *time.Time `json:"finishedAt,omitempty"`
}
