-- +goose Up
CREATE TABLE stock_items (
    branch_id     uuid           NOT NULL,
    consumable_id uuid           NOT NULL,
    name          text           NOT NULL,
    unit          varchar(16)    NOT NULL,
    quantity      numeric(14, 3) NOT NULL DEFAULT 0,
    threshold     numeric(14, 3) NOT NULL DEFAULT 0,
    updated_at    timestamptz    NOT NULL DEFAULT now(),

    PRIMARY KEY (branch_id, consumable_id),
    CONSTRAINT stock_items_quantity_non_negative CHECK (quantity >= 0),
    CONSTRAINT stock_items_threshold_non_negative CHECK (threshold >= 0)
);

CREATE TABLE stock_movements (
    id             uuid PRIMARY KEY,
    branch_id      uuid           NOT NULL,
    consumable_id  uuid           NOT NULL,
    delta          numeric(14, 3) NOT NULL,
    reason         text           NOT NULL,
    appointment_id uuid,
    created_at     timestamptz    NOT NULL DEFAULT now()
);

CREATE INDEX stock_movements_branch ON stock_movements (branch_id, created_at DESC);

-- Inbox: RabbitMQ доставляет at-least-once, и повторная доставка не должна
-- списать материалы дважды. Обработка события начинается со вставки сюда;
-- конфликт означает, что событие уже отработано.
CREATE TABLE processed_events (
    event_id     uuid PRIMARY KEY,
    routing_key  text        NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE processed_events;
DROP TABLE stock_movements;
DROP TABLE stock_items;
