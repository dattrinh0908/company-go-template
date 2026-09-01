package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"order-service/internal/domain"
)

// OrderRepository is the PostgreSQL implementation of domain.OrderRepository.
type OrderRepository struct {
	db *Postgres
}

// Compile-time proof that the port is satisfied. If the interface grows, this
// line breaks at build time instead of at wiring time in main.
var _ domain.OrderRepository = (*OrderRepository)(nil)

// NewOrderRepository wires the repository to a pool.
func NewOrderRepository(db *Postgres) *OrderRepository {
	return &OrderRepository{db: db}
}

// Create inserts an order and its items in one transaction, so an order can
// never be persisted without its lines.
func (r *OrderRepository) Create(ctx context.Context, order *domain.Order) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Rollback after a successful Commit is a no-op, so this is safe to defer
	// unconditionally and covers every early return.
	defer func() { _ = tx.Rollback(ctx) }()

	const insertOrder = `
		INSERT INTO orders (id, customer_id, status, currency, total_amount, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := tx.Exec(ctx, insertOrder,
		order.ID, order.CustomerID, string(order.Status), order.Currency,
		order.TotalAmount, order.CreatedAt, order.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert order: %w", classify(err))
	}

	// CopyFrom is materially faster than one INSERT per line once orders carry
	// more than a handful of items.
	rows := make([][]any, 0, len(order.Items))
	for _, item := range order.Items {
		rows = append(rows, []any{
			item.ID, order.ID, item.SKU, item.Name, item.Quantity, item.UnitPrice,
		})
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"order_items"},
		[]string{"id", "order_id", "sku", "name", "quantity", "unit_price"},
		pgx.CopyFromRows(rows),
	); err != nil {
		return fmt.Errorf("insert order items: %w", classify(err))
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// GetByID loads one order with its items.
func (r *OrderRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	const query = `
		SELECT id, customer_id, status, currency, total_amount, created_at, updated_at
		FROM orders
		WHERE id = $1`

	var order domain.Order
	err := r.db.pool.QueryRow(ctx, query, id).Scan(
		&order.ID, &order.CustomerID, &order.Status, &order.Currency,
		&order.TotalAmount, &order.CreatedAt, &order.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("order %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("select order: %w", classify(err))
	}

	items, err := r.itemsByOrderIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	order.Items = items[id]

	return &order, nil
}

// List returns a filtered, paged set of orders together with the unpaged total.
func (r *OrderRepository) List(ctx context.Context, filter domain.ListFilter) (domain.Page[domain.Order], error) {
	// Predicates are assembled from a fixed set of fragments and the values are
	// always bound as parameters, so this stays injection-safe.
	var (
		where []string
		args  []any
	)
	if filter.CustomerID != nil {
		args = append(args, *filter.CustomerID)
		where = append(where, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if filter.Status != nil {
		args = append(args, string(*filter.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countQuery := "SELECT count(*) FROM orders" + clause
	if err := r.db.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return domain.Page[domain.Order]{}, fmt.Errorf("count orders: %w", classify(err))
	}

	page := domain.Page[domain.Order]{
		Items:  []domain.Order{},
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	}
	if total == 0 {
		return page, nil
	}

	args = append(args, filter.Limit, filter.Offset)
	listQuery := fmt.Sprintf(`
		SELECT id, customer_id, status, currency, total_amount, created_at, updated_at
		FROM orders%s
		ORDER BY created_at DESC, id
		LIMIT $%d OFFSET $%d`, clause, len(args)-1, len(args))

	rows, err := r.db.pool.Query(ctx, listQuery, args...)
	if err != nil {
		return domain.Page[domain.Order]{}, fmt.Errorf("select orders: %w", classify(err))
	}
	defer rows.Close()

	orders := make([]domain.Order, 0, filter.Limit)
	ids := make([]uuid.UUID, 0, filter.Limit)
	for rows.Next() {
		var order domain.Order
		if err := rows.Scan(
			&order.ID, &order.CustomerID, &order.Status, &order.Currency,
			&order.TotalAmount, &order.CreatedAt, &order.UpdatedAt,
		); err != nil {
			return domain.Page[domain.Order]{}, fmt.Errorf("scan order: %w", err)
		}
		orders = append(orders, order)
		ids = append(ids, order.ID)
	}
	if err := rows.Err(); err != nil {
		return domain.Page[domain.Order]{}, fmt.Errorf("iterate orders: %w", err)
	}

	// One query for every page of items rather than one per order: this is the
	// N+1 that listing endpoints usually regress into.
	items, err := r.itemsByOrderIDs(ctx, ids...)
	if err != nil {
		return domain.Page[domain.Order]{}, err
	}
	for i := range orders {
		orders[i].Items = items[orders[i].ID]
	}

	page.Items = orders
	return page, nil
}

// UpdateStatus moves an order to a new status.
func (r *OrderRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.Status) (*domain.Order, error) {
	const query = `
		UPDATE orders
		SET status = $2, updated_at = now()
		WHERE id = $1
		RETURNING id, customer_id, status, currency, total_amount, created_at, updated_at`

	var order domain.Order
	err := r.db.pool.QueryRow(ctx, query, id, string(status)).Scan(
		&order.ID, &order.CustomerID, &order.Status, &order.Currency,
		&order.TotalAmount, &order.CreatedAt, &order.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("order %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("update order status: %w", classify(err))
	}

	items, err := r.itemsByOrderIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	order.Items = items[id]

	return &order, nil
}

// Delete removes an order. Items go with it through ON DELETE CASCADE.
func (r *OrderRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete order: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("order %s: %w", id, domain.ErrNotFound)
	}
	return nil
}

// itemsByOrderIDs loads the lines for every supplied order in a single query.
func (r *OrderRepository) itemsByOrderIDs(ctx context.Context, ids ...uuid.UUID) (map[uuid.UUID][]domain.Item, error) {
	result := make(map[uuid.UUID][]domain.Item, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	const query = `
		SELECT id, order_id, sku, name, quantity, unit_price
		FROM order_items
		WHERE order_id = ANY($1)
		ORDER BY sku`

	rows, err := r.db.pool.Query(ctx, query, ids)
	if err != nil {
		return nil, fmt.Errorf("select order items: %w", classify(err))
	}
	defer rows.Close()

	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(
			&item.ID, &item.OrderID, &item.SKU, &item.Name, &item.Quantity, &item.UnitPrice,
		); err != nil {
			return nil, fmt.Errorf("scan order item: %w", err)
		}
		result[item.OrderID] = append(result[item.OrderID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order items: %w", err)
	}

	return result, nil
}
