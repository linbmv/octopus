package health

import "time"

type RolloutStage struct {
	Name        string
	Percentage  int
	MinDuration time.Duration
	MinRequests int64
}

type RollbackMetrics struct {
	TimeoutRateDelta       float64
	SuccessRateDelta       float64
	FirstTokenP95Delta     float64
	RetryCountDelta        float64
	FallbackRateDelta      float64
	TopChannelStarved      bool
	ErrNoChannel           bool
	PanicObserved          bool
	BusinessIncident       bool
	ObservedRequests       int64
	StageStartedAt         time.Time
}

type RolloutDecision string

const (
	RolloutStay     RolloutDecision = "stay"
	RolloutPromote  RolloutDecision = "promote"
	RolloutRollback RolloutDecision = "rollback"
)

type RolloutController struct {
	Stages []RolloutStage
	Index  int
	Now    func() time.Time
}

func DefaultRolloutController() *RolloutController {
	return &RolloutController{
		Stages: []RolloutStage{
			{Name: "shadow", Percentage: 0, MinDuration: 24 * time.Hour, MinRequests: 5000},
			{Name: "stage-1", Percentage: 1, MinDuration: 24 * time.Hour, MinRequests: 1000},
			{Name: "stage-2", Percentage: 5, MinDuration: 24 * time.Hour, MinRequests: 3000},
			{Name: "stage-3", Percentage: 10, MinDuration: 48 * time.Hour, MinRequests: 10000},
			{Name: "stage-4", Percentage: 25, MinDuration: 48 * time.Hour, MinRequests: 20000},
			{Name: "stage-5", Percentage: 50, MinDuration: 72 * time.Hour, MinRequests: 50000},
			{Name: "stage-6", Percentage: 100, MinDuration: 7 * 24 * time.Hour, MinRequests: 0},
		},
		Now: time.Now,
	}
}

func (c *RolloutController) Current() RolloutStage {
	if c == nil || len(c.Stages) == 0 {
		return RolloutStage{Name: "disabled", Percentage: 0}
	}
	if c.Index < 0 {
		c.Index = 0
	}
	if c.Index >= len(c.Stages) {
		c.Index = len(c.Stages) - 1
	}
	return c.Stages[c.Index]
}

func (c *RolloutController) Evaluate(metrics RollbackMetrics) RolloutDecision {
	if c.shouldRollback(metrics) {
		if c.Index > 0 {
			c.Index--
		}
		return RolloutRollback
	}
	stage := c.Current()
	now := c.Now
	if now == nil {
		now = time.Now
	}
	if !metrics.StageStartedAt.IsZero() && now().Sub(metrics.StageStartedAt) < stage.MinDuration {
		return RolloutStay
	}
	if stage.MinRequests > 0 && metrics.ObservedRequests < stage.MinRequests {
		return RolloutStay
	}
	if c.Index < len(c.Stages)-1 {
		c.Index++
		return RolloutPromote
	}
	return RolloutStay
}

func (c *RolloutController) shouldRollback(metrics RollbackMetrics) bool {
	return metrics.TimeoutRateDelta > 0.05 ||
		metrics.SuccessRateDelta < -0.03 ||
		metrics.FirstTokenP95Delta > 0.20 ||
		metrics.RetryCountDelta > 0.30 ||
		metrics.FallbackRateDelta > 0.50 ||
		metrics.TopChannelStarved ||
		metrics.ErrNoChannel ||
		metrics.PanicObserved ||
		metrics.BusinessIncident
}
