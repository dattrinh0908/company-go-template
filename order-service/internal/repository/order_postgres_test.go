package repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"order-service/internal/config"
	"order-service/internal/domain"
)

// testDB is the pool shared by every test in this package, created once in
// TestMain against a throwaway container.
var testDB *Postgres

func TestMain(m *testing.M) {
	// Integration tests need Docker. Skipping instead of failing keeps
	// `go test ./...` usable on a machine without it.
	if os.Getenv("SKIP_DOCKER_TESTS") != "" {
		os.Exit(0)
	}

	ctx := context.Background()

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("orders"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		panic("start postgres container: " + err.Error())
	}

	host, err := container.Host(ctx)
	if err != nil {
		panic("container host: " + err.Error())
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		panic("container port: " + err.Error())
	}

	testDB, err = NewPostgres(ctx, config.DB{
		Host: host, Port: port.Port(),
		Name: "orders", User: "test", Password: "test",
		Schema: "public", SSLMode: "disable",
		MaxConns: 5, MinConns: 1,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		panic("connect to test database: " + err.Error())
	}

	if err := applyMigrations(ctx); err != nil {
		panic("apply migrations: " + err.Error())
	}

	code := m.Run()

	testDB.Close()
	if err := testcontainers.TerminateContainer(container); err != nil {
		panic("terminate container: " + err.Error())
	}
	os.Exit(code)
}

// applyMigrations runs the checked-in .up.sql files, so the schema under test
// is the same schema that ships.
func applyMigrations(ctx context.Context) error {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) < 7 || name[len(name)-7:] != ".up.sql" {
			continue
		}
		statements, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := testDB.pool.Exec(ctx, string(statements)); err != nil {
			return err
		}
	}
	return nil
}

// truncate resets state between tests so ordering never matters.
func truncate(t *testing.T) {
	t.Helper()
	if _, err := testDB.pool.Exec(context.Background(),
		"TRUNCATE orders, order_items CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedOrder(t *testing.T, repo *OrderRepository, customerID uuid.UUID) *domain.Order {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Microsecond)
	order := &domain.Order{
		ID:          uuid.New(),
		CustomerID:  customerID,
		Status:      domain.StatusPending,
		Currency:    "USD",
		TotalAmount: 3500,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	order.Items = []domain.Item{
		{ID: uuid.New(), OrderID: order.ID, SKU: "SKU-1", Name: "Widget", Quantity: 2, UnitPrice: 1500},
		{ID: uuid.New(), OrderID: order.ID, SKU: "SKU-2", Name: "Gadget", Quantity: 1, UnitPrice: 500},
	}

	if err := repo.Create(context.Background(), order); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return order
}

func TestHealth(t *testing.T) {
	stats := testDB.Health(context.Background())

	if stats["status"] != "up" {
		t.Fatalf("status = %q, want up (error: %s)", stats["status"], stats["error"])
	}
	if _, ok := stats["error"]; ok {
		t.Error("a healthy pool must not report an error")
	}
}

func TestCreateAndGetByID(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	created := seedOrder(t, repo, uuid.New())

	got, err := repo.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got.ID != created.ID || got.CustomerID != created.CustomerID {
		t.Errorf("identity mismatch: got %+v", got)
	}
	if got.TotalAmount != created.TotalAmount {
		t.Errorf("total = %d, want %d", got.TotalAmount, created.TotalAmount)
	}
	if len(got.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(got.Items))
	}
	// itemsByOrderIDs orders by SKU.
	if got.Items[0].SKU != "SKU-1" {
		t.Errorf("items are not ordered by sku: %+v", got.Items)
	}
}

func TestGetByIDNotFound(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	_, err := repo.GetByID(context.Background(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateIsAtomic(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	now := time.Now().UTC()
	order := &domain.Order{
		ID: uuid.New(), CustomerID: uuid.New(), Status: domain.StatusPending,
		Currency: "USD", TotalAmount: 100, CreatedAt: now, UpdatedAt: now,
	}
	// Two lines with the same SKU violate order_items_unique_sku_per_order.
	order.Items = []domain.Item{
		{ID: uuid.New(), OrderID: order.ID, SKU: "DUP", Name: "A", Quantity: 1, UnitPrice: 50},
		{ID: uuid.New(), OrderID: order.ID, SKU: "DUP", Name: "B", Quantity: 1, UnitPrice: 50},
	}

	if err := repo.Create(context.Background(), order); err == nil {
		t.Fatal("expected the duplicate SKU to be rejected")
	}

	// The order row must have been rolled back with the items.
	if _, err := repo.GetByID(context.Background(), order.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("order survived a failed transaction: %v", err)
	}
}

func TestListFiltersAndPages(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	customer := uuid.New()
	for range 3 {
		seedOrder(t, repo, customer)
	}
	other := seedOrder(t, repo, uuid.New())

	t.Run("no filter returns everything", func(t *testing.T) {
		page, err := repo.List(context.Background(), domain.ListFilter{Limit: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if page.Total != 4 {
			t.Errorf("total = %d, want 4", page.Total)
		}
	})

	t.Run("filters by customer", func(t *testing.T) {
		page, err := repo.List(context.Background(), domain.ListFilter{
			CustomerID: &customer, Limit: 10,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if page.Total != 3 || len(page.Items) != 3 {
			t.Errorf("total = %d, items = %d, want 3 and 3", page.Total, len(page.Items))
		}
	})

	t.Run("paging keeps the unpaged total", func(t *testing.T) {
		page, err := repo.List(context.Background(), domain.ListFilter{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page.Items) != 2 {
			t.Errorf("items = %d, want 2", len(page.Items))
		}
		if page.Total != 4 {
			t.Errorf("total = %d, want the unpaged count 4", page.Total)
		}
	})

	t.Run("items are loaded for every row", func(t *testing.T) {
		page, err := repo.List(context.Background(), domain.ListFilter{Limit: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, o := range page.Items {
			if len(o.Items) != 2 {
				t.Errorf("order %s has %d items, want 2", o.ID, len(o.Items))
			}
		}
	})

	t.Run("filters by status", func(t *testing.T) {
		paid := domain.StatusPaid
		if _, err := repo.UpdateStatus(context.Background(), other.ID, paid); err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}

		page, err := repo.List(context.Background(), domain.ListFilter{Status: &paid, Limit: 10})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if page.Total != 1 {
			t.Errorf("total = %d, want 1", page.Total)
		}
	})
}

func TestUpdateStatus(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	order := seedOrder(t, repo, uuid.New())

	updated, err := repo.UpdateStatus(context.Background(), order.ID, domain.StatusPaid)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != domain.StatusPaid {
		t.Errorf("status = %s, want paid", updated.Status)
	}
	if !updated.UpdatedAt.After(order.UpdatedAt) {
		t.Error("expected updated_at to move forward")
	}
	if len(updated.Items) != 2 {
		t.Errorf("items = %d, want the aggregate to come back complete", len(updated.Items))
	}

	if _, err := repo.UpdateStatus(context.Background(), uuid.New(), domain.StatusPaid); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for an unknown id, got %v", err)
	}
}

func TestDeleteCascadesToItems(t *testing.T) {
	truncate(t)
	repo := NewOrderRepository(testDB)

	order := seedOrder(t, repo, uuid.New())

	if err := repo.Delete(context.Background(), order.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var itemCount int
	if err := testDB.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM order_items WHERE order_id = $1", order.ID).Scan(&itemCount); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if itemCount != 0 {
		t.Errorf("%d items survived the cascade", itemCount)
	}

	if err := repo.Delete(context.Background(), order.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound on a second delete, got %v", err)
	}
}
