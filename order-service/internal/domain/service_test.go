package domain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// fakeOrderRepository is an in-memory OrderRepository. The domain package
// declares the port, so a test double needs no mocking framework and no
// database — which is the practical payoff of keeping the interface here.
type fakeOrderRepository struct {
	orders map[uuid.UUID]*Order
	err    error // when set, every method returns it
}

func newFakeRepo() *fakeOrderRepository {
	return &fakeOrderRepository{orders: map[uuid.UUID]*Order{}}
}

func (f *fakeOrderRepository) Create(_ context.Context, order *Order) error {
	if f.err != nil {
		return f.err
	}
	f.orders[order.ID] = order
	return nil
}

func (f *fakeOrderRepository) GetByID(_ context.Context, id uuid.UUID) (*Order, error) {
	if f.err != nil {
		return nil, f.err
	}
	order, ok := f.orders[id]
	if !ok {
		return nil, ErrNotFound
	}
	return order, nil
}

func (f *fakeOrderRepository) List(_ context.Context, filter ListFilter) (Page[Order], error) {
	if f.err != nil {
		return Page[Order]{}, f.err
	}
	items := make([]Order, 0, len(f.orders))
	for _, o := range f.orders {
		items = append(items, *o)
	}
	return Page[Order]{Items: items, Total: len(items), Limit: filter.Limit, Offset: filter.Offset}, nil
}

func (f *fakeOrderRepository) UpdateStatus(_ context.Context, id uuid.UUID, status Status) (*Order, error) {
	if f.err != nil {
		return nil, f.err
	}
	order, ok := f.orders[id]
	if !ok {
		return nil, ErrNotFound
	}
	order.Status = status
	return order, nil
}

func (f *fakeOrderRepository) Delete(_ context.Context, id uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.orders[id]; !ok {
		return ErrNotFound
	}
	delete(f.orders, id)
	return nil
}

func newTestService(repo OrderRepository) *OrderService {
	return NewOrderService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func validInput() CreateOrderInput {
	return CreateOrderInput{
		CustomerID: uuid.New(),
		Currency:   "usd",
		Items: []NewItemInput{
			{SKU: "SKU-1", Name: "Widget", Quantity: 2, UnitPrice: 1500},
			{SKU: "SKU-2", Name: "Gadget", Quantity: 1, UnitPrice: 500},
		},
	}
}

func TestCreateOrderDerivesServerOwnedFields(t *testing.T) {
	svc := newTestService(newFakeRepo())

	order, err := svc.CreateOrder(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if order.ID == uuid.Nil {
		t.Error("expected a generated ID")
	}
	if order.Status != StatusPending {
		t.Errorf("status = %s, want %s", order.Status, StatusPending)
	}
	// The currency is normalised, not echoed.
	if order.Currency != "USD" {
		t.Errorf("currency = %s, want USD", order.Currency)
	}
	// The total is computed from the items, never supplied by the caller.
	if want := int64(3500); order.TotalAmount != want {
		t.Errorf("total = %d, want %d", order.TotalAmount, want)
	}
	if order.CreatedAt.IsZero() || order.UpdatedAt.IsZero() {
		t.Error("expected timestamps to be set")
	}
	for _, item := range order.Items {
		if item.OrderID != order.ID {
			t.Errorf("item %s not linked to its order", item.SKU)
		}
	}
}

func TestCreateOrderRejectsInvalidInputBeforeTouchingRepo(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	in := validInput()
	in.Items[0].Quantity = 0

	if _, err := svc.CreateOrder(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if len(repo.orders) != 0 {
		t.Error("invalid order must not reach the repository")
	}
}

func TestUpdateStatusEnforcesLifecycle(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	order, err := svc.CreateOrder(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// pending -> shipped skips payment and must be rejected.
	if _, err := svc.UpdateStatus(context.Background(), order.ID, StatusShipped); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// pending -> paid is allowed.
	updated, err := svc.UpdateStatus(context.Background(), order.ID, StatusPaid)
	if err != nil {
		t.Fatalf("pending -> paid: %v", err)
	}
	if updated.Status != StatusPaid {
		t.Errorf("status = %s, want %s", updated.Status, StatusPaid)
	}
}

func TestUpdateStatusIsIdempotent(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	order, err := svc.CreateOrder(context.Background(), validInput())
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	// Re-applying the current status is a no-op, not a conflict.
	if _, err := svc.UpdateStatus(context.Background(), order.ID, StatusPending); err != nil {
		t.Fatalf("re-applying the current status should succeed, got %v", err)
	}
}

func TestUpdateStatusRejectsUnknownStatus(t *testing.T) {
	svc := newTestService(newFakeRepo())

	_, err := svc.UpdateStatus(context.Background(), uuid.New(), Status("refunded"))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestGetOrderPropagatesNotFound(t *testing.T) {
	svc := newTestService(newFakeRepo())

	if _, err := svc.GetOrder(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListOrdersClampsLimit(t *testing.T) {
	repo := newFakeRepo()
	svc := newTestService(repo)

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero falls back to the default", 0, DefaultPageLimit},
		{"negative falls back to the default", -10, DefaultPageLimit},
		{"oversized is clamped to the maximum", MaxPageLimit + 500, MaxPageLimit},
		{"a sane value is preserved", 25, 25},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := svc.ListOrders(context.Background(), ListFilter{Limit: tt.limit})
			if err != nil {
				t.Fatalf("ListOrders: %v", err)
			}
			if page.Limit != tt.want {
				t.Errorf("limit = %d, want %d", page.Limit, tt.want)
			}
		})
	}
}
