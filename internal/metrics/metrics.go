package metrics

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "octopus",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		},
		[]string{"method", "route", "status"},
	)
	HTTPRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "octopus",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)
	HTTPRequestsInFlight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "octopus",
			Subsystem: "http",
			Name:      "requests_in_flight",
			Help:      "Current number of HTTP requests being served.",
		},
	)
	RelayRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "octopus",
			Subsystem: "relay",
			Name:      "requests_total",
			Help:      "Total number of completed relay requests.",
		},
		[]string{"success", "channel_id"},
	)
	RelayRequestDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "octopus",
			Subsystem: "relay",
			Name:      "request_duration_seconds",
			Help:      "Relay request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
	)
	CircuitBreakerEntries = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "octopus",
			Subsystem: "circuit",
			Name:      "breaker_entries",
			Help:      "Circuit breaker entries by state, refreshed on each metrics scrape.",
		},
		[]string{"state"},
	)
	RelayLogDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "octopus",
			Subsystem: "relay_log",
			Name:      "dropped_total",
			Help:      "Relay log events dropped or evicted from bounded notification, subscriber, or memory buffers.",
		},
		[]string{"reason"},
	)
	RelayLogFlushFailuresTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "octopus",
			Subsystem: "relay_log",
			Name:      "flush_failures_total",
			Help:      "Total number of failed relay log database flush attempts.",
		},
	)
)

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		HTTPRequestsInFlight.Inc()
		defer HTTPRequestsInFlight.Dec()

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		HTTPRequestTotal.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		HTTPRequestDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(started).Seconds())
	}
}

func Handler() http.Handler {
	return promhttp.Handler()
}

func RecordRelay(success bool, channelID int, duration time.Duration) {
	RelayRequestsTotal.WithLabelValues(strconv.FormatBool(success), strconv.Itoa(channelID)).Inc()
	RelayRequestDuration.Observe(duration.Seconds())
}

func RecordRelayLogDropped(reason string, count uint64) {
	if count == 0 {
		return
	}
	RelayLogDroppedTotal.WithLabelValues(reason).Add(float64(count))
}

func RecordRelayLogFlushFailure() {
	RelayLogFlushFailuresTotal.Inc()
}

type DBCollector struct {
	db           *sql.DB
	maxOpen      *prometheus.Desc
	open         *prometheus.Desc
	inUse        *prometheus.Desc
	idle         *prometheus.Desc
	waitCount    *prometheus.Desc
	waitDuration *prometheus.Desc
}

func NewDBCollector(db *sql.DB) *DBCollector {
	return &DBCollector{
		db:           db,
		maxOpen:      prometheus.NewDesc("octopus_db_max_open_connections", "Maximum number of open database connections.", nil, nil),
		open:         prometheus.NewDesc("octopus_db_open_connections", "Current number of open database connections.", nil, nil),
		inUse:        prometheus.NewDesc("octopus_db_in_use_connections", "Current number of database connections in use.", nil, nil),
		idle:         prometheus.NewDesc("octopus_db_idle_connections", "Current number of idle database connections.", nil, nil),
		waitCount:    prometheus.NewDesc("octopus_db_wait_count_total", "Total number of waits for a database connection.", nil, nil),
		waitDuration: prometheus.NewDesc("octopus_db_wait_duration_seconds_total", "Total time blocked waiting for a database connection.", nil, nil),
	}
}

func RegisterDB(db *sql.DB) error {
	err := prometheus.Register(NewDBCollector(db))
	if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
		return nil
	}
	return err
}

func (c *DBCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.maxOpen
	ch <- c.open
	ch <- c.inUse
	ch <- c.idle
	ch <- c.waitCount
	ch <- c.waitDuration
}

func (c *DBCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil || c.db == nil {
		return
	}
	stats := c.db.Stats()
	ch <- prometheus.MustNewConstMetric(c.maxOpen, prometheus.GaugeValue, float64(stats.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(c.open, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(stats.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(stats.Idle))
	ch <- prometheus.MustNewConstMetric(c.waitCount, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitDuration, prometheus.CounterValue, stats.WaitDuration.Seconds())
}
