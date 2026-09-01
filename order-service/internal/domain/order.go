// Package domain holds the business model: entities, their invariants, the
// repository ports they are persisted through, and the services that
// orchestrate them.
//
// It is the innermost layer and depends on nothing outside the standard
// library plus a UUID type. No Gin, no pgx, no SQL. That constraint is what
// lets the business rules be unit-tested without a database or an HTTP server,
// and it is worth defending in code review.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status is the lifecycle state of an Order.
type Status string

// The order lifecycle.
const (
	StatusPending   Status = "pending"
	StatusPaid      Status = "paid"
	StatusShipped   Status = "shipped"
	StatusDelivered Status = "delivered"
	StatusCancelled Status = "cancelled"
)

// allowedTransitions encodes the state machine. A status missing from this map
// is terminal.
var allowedTransitions = map[Status][]Status{
	StatusPending: {StatusPaid, StatusCancelled},
	StatusPaid:    {StatusShipped, StatusCancelled},
	StatusShipped: {StatusDelivered},
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPaid, StatusShipped, StatusDelivered, StatusCancelled:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether moving from s to next is permitted.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range allowedTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (s Status) String() string { return string(s) }

// Item is a single line on an order.
type Item struct {
	ID        uuid.UUID `json:"id"`
	OrderID   uuid.UUID `json:"order_id"`
	SKU       string    `json:"sku"`
	Name      string    `json:"name"`
	Quantity  int       `json:"quantity"`
	UnitPrice int64     `json:"unit_price"`
}

// Subtotal is the line total in minor currency units.
func (i Item) Subtotal() int64 { return int64(i.Quantity) * i.UnitPrice }

// Order is the aggregate root.
//
// Money is stored in minor units (cents) as an int64 rather than a float, so
// arithmetic is exact. Currency is an ISO-4217 code.
type Order struct {
	ID          uuid.UUID `json:"id"`
	CustomerID  uuid.UUID `json:"customer_id"`
	Status      Status    `json:"status"`
	Currency    string    `json:"currency"`
	TotalAmount int64     `json:"total_amount"`
	Items       []Item    `json:"items"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Total recomputes the order total from its items.
func (o *Order) Total() int64 {
	var sum int64
	for _, item := range o.Items {
		sum += item.Subtotal()
	}
	return sum
}

// Validate checks every invariant of the aggregate and reports all violations
// at once.
func (o *Order) Validate() error {
	v := &ValidationError{}

	if o.CustomerID == uuid.Nil {
		v.Add("customer_id", "must be a non-empty UUID")
	}
	if len(o.Currency) != 3 {
		v.Add("currency", "must be a 3-letter ISO-4217 code")
	}
	if !o.Status.Valid() {
		v.Add("status", "must be one of pending, paid, shipped, delivered, cancelled")
	}
	if len(o.Items) == 0 {
		v.Add("items", "an order must contain at least one item")
	}

	seen := make(map[string]struct{}, len(o.Items))
	for idx, item := range o.Items {
		field := func(name string) string {
			return "items[" + itoa(idx) + "]." + name
		}
		sku := strings.TrimSpace(item.SKU)
		if sku == "" {
			v.Add(field("sku"), "must not be empty")
		} else if _, dup := seen[sku]; dup {
			v.Add(field("sku"), "duplicated within the same order")
		} else {
			seen[sku] = struct{}{}
		}
		if strings.TrimSpace(item.Name) == "" {
			v.Add(field("name"), "must not be empty")
		}
		if item.Quantity < 1 {
			v.Add(field("quantity"), "must be at least 1")
		}
		if item.UnitPrice < 0 {
			v.Add(field("unit_price"), "must not be negative")
		}
	}

	return v.OrNil()
}

// itoa avoids pulling strconv into the hot validation path for small indices.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
