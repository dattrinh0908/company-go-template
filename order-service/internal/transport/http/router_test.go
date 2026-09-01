package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"order-service/internal/config"
	"order-service/internal/domain"
	"order-service/internal/observability"
)

// --- test doubles --------------------------------------------------------

type stubRepo struct {
	orders map[uuid.UUID]*domain.Order
}

func newStubRepo() *stubRepo { return &stubRepo{orders: map[uuid.UUID]*domain.Order{}} }

func (s *stubRepo) Create(_ context.Context, order *domain.Order) error {
	s.orders[order.ID] = order
	return nil
}

func (s *stubRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Order, error) {
	order, ok := s.orders[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return order, nil
}

func (s *stubRepo) List(_ context.Context, filter domain.ListFilter) (domain.Page[domain.Order], error) {
	items := make([]domain.Order, 0, len(s.orders))
	for _, o := range s.orders {
		items = append(items, *o)
	}
	return domain.Page[domain.Order]{
		Items: items, Total: len(items), Limit: filter.Limit, Offset: filter.Offset,
	}, nil
}

func (s *stubRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.Status) (*domain.Order, error) {
	order, ok := s.orders[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	order.Status = status
	return order, nil
}

func (s *stubRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := s.orders[id]; !ok {
		return domain.ErrNotFound
	}
	delete(s.orders, id)
	return nil
}

// stubHealth reports whatever status the test asks for.
type stubHealth struct{ status string }

func (s stubHealth) Health(context.Context) map[string]string {
	return map[string]string{"status": s.status}
}

func newTestRouter(t *testing.T, repo domain.OrderRepository, health domain.HealthChecker) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := observability.NewHTTPMetrics(metricnoop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatalf("NewHTTPMetrics: %v", err)
	}

	return NewRouter(Deps{
		Config: config.Config{
			App:  config.App{Name: "order-service", Env: "test", Version: "test"},
			HTTP: config.HTTP{CORSOrigins: []string{"*"}},
		},
		Logger:  logger,
		Metrics: metrics,
		Orders:  domain.NewOrderService(repo, logger),
		DB:      health,
	})
}

func do(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// --- tests ---------------------------------------------------------------

func TestHealthProbes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		dbStatus   string
		wantStatus int
	}{
		{"livez ignores dependencies", "/livez", "down", http.StatusOK},
		{"readyz is ok when the database is up", "/readyz", "up", http.StatusOK},
		{"readyz is unavailable when the database is down", "/readyz", "down", http.StatusServiceUnavailable},
		{"health aliases readyz", "/health", "up", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter(t, newStubRepo(), stubHealth{status: tt.dbStatus})

			rec := do(t, r, http.MethodGet, tt.path, nil)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestEveryResponseCarriesARequestID(t *testing.T) {
	r := newTestRouter(t, newStubRepo(), stubHealth{status: "up"})

	rec := do(t, r, http.MethodGet, "/livez", nil)
	if rec.Header().Get(requestIDHeader) == "" {
		t.Errorf("expected a %s header on every response", requestIDHeader)
	}
}

func TestCreateOrderRoundTrip(t *testing.T) {
	r := newTestRouter(t, newStubRepo(), stubHealth{status: "up"})

	rec := do(t, r, http.MethodPost, "/api/v1/orders", map[string]any{
		"customer_id": uuid.NewString(),
		"currency":    "USD",
		"items": []map[string]any{
			{"sku": "SKU-1", "name": "Widget", "quantity": 2, "unit_price": 1500},
		},
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body)
	}

	var order domain.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if order.TotalAmount != 3000 {
		t.Errorf("total = %d, want 3000", order.TotalAmount)
	}
	if order.Status != domain.StatusPending {
		t.Errorf("status = %s, want pending", order.Status)
	}
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Error("expected a Location header on 201")
	}
}

func TestErrorMapping(t *testing.T) {
	repo := newStubRepo()
	r := newTestRouter(t, repo, stubHealth{status: "up"})

	// Seed one order so the conflict case has something to transition.
	created := do(t, r, http.MethodPost, "/api/v1/orders", map[string]any{
		"customer_id": uuid.NewString(),
		"currency":    "USD",
		"items":       []map[string]any{{"sku": "S", "name": "N", "quantity": 1, "unit_price": 10}},
	})
	var seeded domain.Order
	if err := json.Unmarshal(created.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seed: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
	}{
		{
			name:   "unknown id is 404",
			method: http.MethodGet, path: "/api/v1/orders/" + uuid.NewString(),
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
		{
			name:   "malformed id is 422",
			method: http.MethodGet, path: "/api/v1/orders/not-a-uuid",
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
		{
			name:   "missing required fields is 422",
			method: http.MethodPost, path: "/api/v1/orders",
			body:       map[string]any{"currency": "USD"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
		{
			name:   "forbidden transition is 409",
			method: http.MethodPatch, path: "/api/v1/orders/" + seeded.ID.String() + "/status",
			body:       map[string]any{"status": "shipped"},
			wantStatus: http.StatusConflict, wantCode: "conflict",
		},
		{
			name:   "unknown status is 422",
			method: http.MethodPatch, path: "/api/v1/orders/" + seeded.ID.String() + "/status",
			body:       map[string]any{"status": "refunded"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
		{
			name:   "unmatched route is 404",
			method: http.MethodGet, path: "/api/v1/nope",
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, r, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body)
			}

			var resp errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if resp.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", resp.Error.Code, tt.wantCode)
			}
			if resp.Error.RequestID == "" {
				t.Error("expected the request ID to be echoed in the error body")
			}
		})
	}
}

func TestPanicIsRecoveredAsInternalError(t *testing.T) {
	r := newTestRouter(t, newStubRepo(), stubHealth{status: "up"})
	r.GET("/boom", func(*gin.Context) { panic("boom") })

	rec := do(t, r, http.MethodGet, "/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// The panic value must never leak to the client.
	if bytes.Contains(rec.Body.Bytes(), []byte("boom")) {
		t.Error("panic detail leaked into the response body")
	}
	if resp.Error.Code != "internal_error" {
		t.Errorf("code = %q, want internal_error", resp.Error.Code)
	}
}
