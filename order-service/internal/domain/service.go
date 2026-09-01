package domain

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Default and maximum page sizes for listings.
const (
	DefaultPageLimit = 20
	MaxPageLimit     = 100
)

// OrderService holds the use cases for the Order aggregate. Handlers call it;
// it calls the repository. Business rules that span more than a single entity
// live here, invariants of a single entity live on the entity itself.
type OrderService struct {
	repo   OrderRepository
	logger *slog.Logger
	now    func() time.Time
}

// NewOrderService wires a service. Passing the clock as a field keeps
// time-dependent behaviour testable without a global.
func NewOrderService(repo OrderRepository, logger *slog.Logger) *OrderService {
	return &OrderService{
		repo:   repo,
		logger: logger,
		now:    time.Now().UTC,
	}
}

// NewItemInput is one requested line on a new order.
type NewItemInput struct {
	SKU       string
	Name      string
	Quantity  int
	UnitPrice int64
}

// CreateOrderInput is the use-case payload for placing an order. It is a
// separate type from Order on purpose: the caller does not get to choose the
// ID, the status, or the timestamps.
type CreateOrderInput struct {
	CustomerID uuid.UUID
	Currency   string
	Items      []NewItemInput
}

// CreateOrder validates the request, derives the server-owned fields and
// persists the aggregate.
func (s *OrderService) CreateOrder(ctx context.Context, in CreateOrderInput) (*Order, error) {
	now := s.now()
	order := &Order{
		ID:         uuid.New(),
		CustomerID: in.CustomerID,
		Status:     StatusPending,
		Currency:   strings.ToUpper(strings.TrimSpace(in.Currency)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	order.Items = make([]Item, 0, len(in.Items))
	for _, item := range in.Items {
		order.Items = append(order.Items, Item{
			ID:        uuid.New(),
			OrderID:   order.ID,
			SKU:       strings.TrimSpace(item.SKU),
			Name:      strings.TrimSpace(item.Name),
			Quantity:  item.Quantity,
			UnitPrice: item.UnitPrice,
		})
	}

	// The total is derived, never taken from the client.
	order.TotalAmount = order.Total()

	if err := order.Validate(); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	s.logger.InfoContext(ctx, "order created",
		slog.String("order_id", order.ID.String()),
		slog.String("customer_id", order.CustomerID.String()),
		slog.Int64("total_amount", order.TotalAmount),
		slog.String("currency", order.Currency),
	)
	return order, nil
}

// GetOrder returns one order by ID.
func (s *OrderService) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	order, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get order %s: %w", id, err)
	}
	return order, nil
}

// ListOrders returns a page of orders, clamping the page size so a caller
// cannot ask for the whole table.
func (s *OrderService) ListOrders(ctx context.Context, filter ListFilter) (Page[Order], error) {
	switch {
	case filter.Limit <= 0:
		filter.Limit = DefaultPageLimit
	case filter.Limit > MaxPageLimit:
		filter.Limit = MaxPageLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	if filter.Status != nil && !filter.Status.Valid() {
		v := &ValidationError{}
		v.Add("status", "unknown status "+filter.Status.String())
		return Page[Order]{}, v
	}

	page, err := s.repo.List(ctx, filter)
	if err != nil {
		return Page[Order]{}, fmt.Errorf("list orders: %w", err)
	}
	return page, nil
}

// UpdateStatus enforces the lifecycle state machine before persisting.
func (s *OrderService) UpdateStatus(ctx context.Context, id uuid.UUID, next Status) (*Order, error) {
	if !next.Valid() {
		v := &ValidationError{}
		v.Add("status", "unknown status "+next.String())
		return nil, v
	}

	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get order %s: %w", id, err)
	}
	if current.Status == next {
		// Idempotent: re-applying the current status is not an error.
		return current, nil
	}
	if !current.Status.CanTransitionTo(next) {
		return nil, fmt.Errorf("cannot move order %s from %s to %s: %w",
			id, current.Status, next, ErrConflict)
	}

	updated, err := s.repo.UpdateStatus(ctx, id, next)
	if err != nil {
		return nil, fmt.Errorf("update status of order %s: %w", id, err)
	}

	s.logger.InfoContext(ctx, "order status changed",
		slog.String("order_id", id.String()),
		slog.String("from", current.Status.String()),
		slog.String("to", next.String()),
	)
	return updated, nil
}

// CancelOrder is a named shortcut for the most common transition.
func (s *OrderService) CancelOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	return s.UpdateStatus(ctx, id, StatusCancelled)
}

// DeleteOrder removes an order permanently. Most businesses prefer cancelling;
// this exists for administrative cleanup and data-retention work.
func (s *OrderService) DeleteOrder(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete order %s: %w", id, err)
	}
	s.logger.InfoContext(ctx, "order deleted", slog.String("order_id", id.String()))
	return nil
}
