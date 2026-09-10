package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mireacrm/go-common/infra"
)

const negativeQuantity = "stock_items_quantity_non_negative"

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

// ApplyMovements выполняет все движения одной транзакцией: частично списанный
// набор материалов хуже, чем не списанный вовсе. Отметка об обработке события
// пишется той же транзакцией — иначе упавшее списание оставит событие
// помеченным, и повторная доставка молча пропустит его.
func (r *PostgresRepository) ApplyMovements(
	ctx context.Context, eventID *uuid.UUID, routingKey string, movements []Movement,
) ([]StockItem, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("транзакция: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if eventID != nil {
		tag, err := tx.Exec(ctx,
			`INSERT INTO processed_events (event_id, routing_key) VALUES ($1, $2)
			 ON CONFLICT (event_id) DO NOTHING`, *eventID, routingKey)
		if err != nil {
			return nil, false, fmt.Errorf("отметка события: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, false, nil
		}
	}

	items := make([]StockItem, 0, len(movements))
	for _, movement := range movements {
		item, err := applyOne(ctx, tx, movement)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("коммит: %w", err)
	}
	return items, true, nil
}

const columns = "branch_id, consumable_id, name, unit, quantity, threshold, updated_at"

// Пополнение создаёт позицию, списание — только уменьшает существующую.
//
// Одним upsert это не делается: CHECK (quantity >= 0) проверяется на
// предлагаемой строке ДО разрешения конфликта, а ON CONFLICT перехватывает
// только нарушения уникальности. При списании Postgres увидит quantity = -60
// и упадёт, не дойдя до DO UPDATE.
func applyOne(ctx context.Context, tx pgx.Tx, movement Movement) (StockItem, error) {
	var item StockItem
	var err error

	if movement.Delta >= 0 {
		item, err = replenishOne(ctx, tx, movement)
	} else {
		item, err = writeOffOne(ctx, tx, movement)
	}
	if err != nil {
		return StockItem{}, err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO stock_movements (id, branch_id, consumable_id, delta, reason, appointment_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), movement.BranchID, movement.ConsumableID,
		movement.Delta, movement.Reason, movement.AppointmentID)
	if err != nil {
		return StockItem{}, fmt.Errorf("история движений: %w", err)
	}
	return item, nil
}

func replenishOne(ctx context.Context, tx pgx.Tx, movement Movement) (StockItem, error) {
	query := `
		INSERT INTO stock_items (branch_id, consumable_id, name, unit, quantity)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (branch_id, consumable_id) DO UPDATE
		SET quantity = stock_items.quantity + EXCLUDED.quantity,
		    name = EXCLUDED.name,
		    unit = EXCLUDED.unit,
		    updated_at = now()
		RETURNING ` + columns

	item, err := scanItem(tx.QueryRow(ctx, query,
		movement.BranchID, movement.ConsumableID, movement.Name, movement.Unit, movement.Delta))
	if err != nil {
		return StockItem{}, fmt.Errorf("пополнение склада: %w", err)
	}
	return item, nil
}

func writeOffOne(ctx context.Context, tx pgx.Tx, movement Movement) (StockItem, error) {
	query := `
		UPDATE stock_items
		SET quantity = quantity + $3, updated_at = now()
		WHERE branch_id = $1 AND consumable_id = $2 AND quantity + $3 >= 0
		RETURNING ` + columns

	item, err := scanItem(tx.QueryRow(ctx, query,
		movement.BranchID, movement.ConsumableID, movement.Delta))
	if errors.Is(err, pgx.ErrNoRows) {
		// Различаем «позиции нет вовсе» и «остатка не хватает»: для
		// администратора это разные проблемы.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM stock_items WHERE branch_id = $1 AND consumable_id = $2`,
			movement.BranchID, movement.ConsumableID).Scan(&exists); err != nil {
			return StockItem{}, infra.Conflict("%s не заведён на складе филиала", movement.Name)
		}
		return StockItem{}, infra.Conflict("на складе недостаточно %q", movement.Name)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == negativeQuantity {
		return StockItem{}, infra.Conflict("на складе недостаточно %q", movement.Name)
	}
	if err != nil {
		return StockItem{}, fmt.Errorf("списание со склада: %w", err)
	}
	return item, nil
}

func scanItem(row pgx.Row) (StockItem, error) {
	var item StockItem
	err := row.Scan(&item.BranchID, &item.ConsumableID, &item.Name, &item.Unit,
		&item.Quantity, &item.Threshold, &item.UpdatedAt)
	return item, err
}

func (r *PostgresRepository) List(ctx context.Context, branchID uuid.UUID) ([]StockItem, error) {
	return r.query(ctx,
		`SELECT branch_id, consumable_id, name, unit, quantity, threshold, updated_at
		 FROM stock_items WHERE branch_id = $1 ORDER BY name`, branchID)
}

func (r *PostgresRepository) ListLow(ctx context.Context, branchID uuid.UUID) ([]StockItem, error) {
	return r.query(ctx,
		`SELECT branch_id, consumable_id, name, unit, quantity, threshold, updated_at
		 FROM stock_items WHERE branch_id = $1 AND quantity < threshold ORDER BY name`, branchID)
}

func (r *PostgresRepository) SetThreshold(
	ctx context.Context, branchID, consumableID uuid.UUID, threshold float64,
) (StockItem, error) {
	const query = `
		UPDATE stock_items SET threshold = $3, updated_at = now()
		WHERE branch_id = $1 AND consumable_id = $2
		RETURNING branch_id, consumable_id, name, unit, quantity, threshold, updated_at`

	var item StockItem
	err := r.pool.QueryRow(ctx, query, branchID, consumableID, threshold).Scan(
		&item.BranchID, &item.ConsumableID, &item.Name, &item.Unit,
		&item.Quantity, &item.Threshold, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StockItem{}, infra.NotFound("stock item", consumableID)
	}
	if err != nil {
		return StockItem{}, fmt.Errorf("установка порога: %w", err)
	}
	return item, nil
}

func (r *PostgresRepository) query(
	ctx context.Context, sql string, args ...any,
) ([]StockItem, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("чтение склада: %w", err)
	}
	defer rows.Close()

	items := make([]StockItem, 0)
	for rows.Next() {
		var item StockItem
		if err := rows.Scan(&item.BranchID, &item.ConsumableID, &item.Name, &item.Unit,
			&item.Quantity, &item.Threshold, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
