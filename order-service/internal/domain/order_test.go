package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func validOrder() *Order {
	return &Order{
		ID:         uuid.New(),
		CustomerID: uuid.New(),
		Status:     StatusPending,
		Currency:   "USD",
		Items: []Item{
			{ID: uuid.New(), SKU: "SKU-1", Name: "Widget", Quantity: 2, UnitPrice: 1500},
		},
	}
}

func TestStatusCanTransitionTo(t *testing.T) {
	tests := []struct {
		name string
		from Status
		to   Status
		want bool
	}{
		{"pending to paid", StatusPending, StatusPaid, true},
		{"pending to cancelled", StatusPending, StatusCancelled, true},
		{"pending to shipped skips payment", StatusPending, StatusShipped, false},
		{"paid to shipped", StatusPaid, StatusShipped, true},
		{"shipped to delivered", StatusShipped, StatusDelivered, true},
		{"shipped cannot be cancelled", StatusShipped, StatusCancelled, false},
		{"delivered is terminal", StatusDelivered, StatusPaid, false},
		{"cancelled is terminal", StatusCancelled, StatusPending, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
				t.Errorf("%s -> %s: got %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestOrderTotal(t *testing.T) {
	order := &Order{Items: []Item{
		{Quantity: 2, UnitPrice: 1500},
		{Quantity: 3, UnitPrice: 200},
	}}

	if got, want := order.Total(), int64(3600); got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
}

func TestOrderValidate(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Order)
		wantFields []string
	}{
		{
			name:   "valid order passes",
			mutate: func(*Order) {},
		},
		{
			name:       "missing customer",
			mutate:     func(o *Order) { o.CustomerID = uuid.Nil },
			wantFields: []string{"customer_id"},
		},
		{
			name:       "bad currency",
			mutate:     func(o *Order) { o.Currency = "DOLLAR" },
			wantFields: []string{"currency"},
		},
		{
			name:       "no items",
			mutate:     func(o *Order) { o.Items = nil },
			wantFields: []string{"items"},
		},
		{
			name:       "zero quantity",
			mutate:     func(o *Order) { o.Items[0].Quantity = 0 },
			wantFields: []string{"items[0].quantity"},
		},
		{
			name:       "negative price",
			mutate:     func(o *Order) { o.Items[0].UnitPrice = -1 },
			wantFields: []string{"items[0].unit_price"},
		},
		{
			name: "duplicate sku",
			mutate: func(o *Order) {
				o.Items = append(o.Items, Item{SKU: "SKU-1", Name: "Widget", Quantity: 1, UnitPrice: 100})
			},
			wantFields: []string{"items[1].sku"},
		},
		{
			name: "reports every problem at once",
			mutate: func(o *Order) {
				o.CustomerID = uuid.Nil
				o.Currency = ""
				o.Items[0].Quantity = -5
			},
			wantFields: []string{"customer_id", "currency", "items[0].quantity"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := validOrder()
			tt.mutate(order)

			err := order.Validate()
			if len(tt.wantFields) == 0 {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("error should unwrap to ErrInvalidInput, got %v", err)
			}

			var v *ValidationError
			if !errors.As(err, &v) {
				t.Fatalf("expected *ValidationError, got %T", err)
			}

			got := make(map[string]bool, len(v.Fields))
			for _, f := range v.Fields {
				got[f.Field] = true
			}
			for _, want := range tt.wantFields {
				if !got[want] {
					t.Errorf("missing field %q in %v", want, v.Fields)
				}
			}
		})
	}
}
