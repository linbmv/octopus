package metrics

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	_ "modernc.org/sqlite"
)

func TestMiddlewareRecordsRequestMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(Middleware())
	router.GET("/items/:id", func(c *gin.Context) { c.Status(http.StatusCreated) })

	before := testutil.ToFloat64(HTTPRequestTotal.WithLabelValues(http.MethodGet, "/items/:id", "201"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items/7", nil))
	after := testutil.ToFloat64(HTTPRequestTotal.WithLabelValues(http.MethodGet, "/items/:id", "201"))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if after-before != 1 {
		t.Fatalf("request counter delta = %v, want 1", after-before)
	}
}

func TestHandlerExposesOctopusMetrics(t *testing.T) {
	RecordRelay(true, 7, 0)
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "octopus_relay_requests_total") {
		t.Fatal("metrics response does not contain relay metrics")
	}
}

func TestDBCollector(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(5)

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewDBCollector(database))
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if len(families) != 6 {
		t.Fatalf("metric family count = %d, want 6", len(families))
	}
}

func TestRelayLogDropAndFlushMetrics(t *testing.T) {
	dropped := RelayLogDroppedTotal.WithLabelValues("test_overflow")
	beforeDropped := testutil.ToFloat64(dropped)
	beforeFailures := testutil.ToFloat64(RelayLogFlushFailuresTotal)

	RecordRelayLogDropped("test_overflow", 3)
	RecordRelayLogFlushFailure()

	if delta := testutil.ToFloat64(dropped) - beforeDropped; delta != 3 {
		t.Fatalf("relay log dropped counter delta = %v, want 3", delta)
	}
	if delta := testutil.ToFloat64(RelayLogFlushFailuresTotal) - beforeFailures; delta != 1 {
		t.Fatalf("relay log flush failure counter delta = %v, want 1", delta)
	}
}
