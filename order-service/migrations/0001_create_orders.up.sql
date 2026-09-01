-- 0001_create_orders (up)
--
-- Money is stored in minor units (cents) as BIGINT rather than NUMERIC or
-- FLOAT: exact arithmetic, no rounding surprises, and it matches the int64 the
-- domain uses.

CREATE TABLE IF NOT EXISTS orders (
    id           UUID        PRIMARY KEY,
    customer_id  UUID        NOT NULL,
    status       TEXT        NOT NULL,
    currency     CHAR(3)     NOT NULL,
    total_amount BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The lifecycle is enforced in the domain; this constraint is the
    -- last line of defence against a bad write from anywhere else.
    CONSTRAINT orders_status_check
        CHECK (status IN ('pending', 'paid', 'shipped', 'delivered', 'cancelled')),
    CONSTRAINT orders_total_amount_check CHECK (total_amount >= 0)
);

CREATE TABLE IF NOT EXISTS order_items (
    id         UUID   PRIMARY KEY,
    order_id   UUID   NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    sku        TEXT   NOT NULL,
    name       TEXT   NOT NULL,
    quantity   INT    NOT NULL,
    unit_price BIGINT NOT NULL,

    CONSTRAINT order_items_quantity_check CHECK (quantity > 0),
    CONSTRAINT order_items_unit_price_check CHECK (unit_price >= 0),
    -- Matches the duplicate-SKU rule in domain.Order.Validate.
    CONSTRAINT order_items_unique_sku_per_order UNIQUE (order_id, sku)
);

-- Supports the customer_id filter and the created_at DESC ordering used by the
-- listing endpoint.
CREATE INDEX IF NOT EXISTS orders_customer_id_created_at_idx
    ON orders (customer_id, created_at DESC);

CREATE INDEX IF NOT EXISTS orders_status_idx
    ON orders (status);

-- Every item lookup is by order_id; the composite unique constraint above
-- cannot serve it because order_id is not its leading column in all planners'
-- estimates for this access pattern.
CREATE INDEX IF NOT EXISTS order_items_order_id_idx
    ON order_items (order_id);
