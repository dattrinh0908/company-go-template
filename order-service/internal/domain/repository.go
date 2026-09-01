package domain

import (
	"context"

	"github.com/google/uuid"
)

// ListFilter narrows and pages a listing query. Nil pointer fields mean "do not
// filter on this attribute".
type ListFilter struct {
	CustomerID *uuid.UUID
	Status     *Status
	Limit      int
	Offset     int
}

// Page is one page of results plus the total number of matching rows, so a
// caller can render pagination controls without a second round trip.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// OrderRepository is the persistence port for the Order aggregate.
//
// It is declared here, in the domain, and implemented in internal/repository.
// That direction is the whole point of the layout: the database depends on the
// business model, never the reverse. Swapping Postgres for anything else means
// writing one new implementation and changing one line in main.
type OrderRepository interface {
	// Create persists a new order together with its items. Implementations
	// must be atomic across both.
	Create(ctx context.Context, order *Order) error

	// GetByID returns the order and its items, or ErrNotFound.
	GetByID(ctx context.Context, id uuid.UUID) (*Order, error)

	// List returns matching orders and the total count before paging.
	List(ctx context.Context, filter ListFilter) (Page[Order], error)

	// UpdateStatus moves an order to a new status and returns the updated
	// aggregate, or ErrNotFound.
	UpdateStatus(ctx context.Context, id uuid.UUID, status Status) (*Order, error)

	// Delete removes an order and its items, or returns ErrNotFound.
	Delete(ctx context.Context, id uuid.UUID) error
}

// HealthChecker is implemented by any dependency that can report readiness.
// Keeping it here lets the HTTP health handler depend on the domain rather
// than on a concrete database type.
type HealthChecker interface {
	Health(ctx context.Context) map[string]string
}
