//go:build integration

package inventory

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/mireacrm/go-common/infra"
)

func quantity(t *testing.T, repo *PostgresRepository, branch, consumable uuid.UUID) float64 {
	t.Helper()
	items, err := repo.List(context.Background(), branch)
	if err != nil {
		t.Fatalf("остатки: %v", err)
	}
	for _, item := range items {
		if item.ConsumableID == consumable {
			return item.Quantity
		}
	}
	return -1
}

func TestReplenishCreatesThenAccumulates(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()

	for _, amount := range []float64{100, 50} {
		if _, _, err := repo.ApplyMovements(
			ctx, nil, "", []Movement{replenish(branch, consumable, amount)},
		); err != nil {
			t.Fatalf("пополнение: %v", err)
		}
	}

	if got := quantity(t, repo, branch, consumable); got != 150 {
		t.Fatalf("остаток %v вместо 150", got)
	}
}

func TestWriteOffDecrements(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 100)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{writeOff(branch, consumable, 60)},
	); err != nil {
		t.Fatalf("списание: %v", err)
	}

	if got := quantity(t, repo, branch, consumable); got != 40 {
		t.Fatalf("остаток %v вместо 40", got)
	}
}

// Списание не должно создавать позицию: списать то, чего на складе нет,
// нельзя по смыслу. Одним upsert это не делается — CHECK (quantity >= 0)
// проверяется на предлагаемой строке до разрешения конфликта.
func TestWriteOffOnMissingItemRejected(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()

	_, _, err := repo.ApplyMovements(ctx, nil, "", []Movement{writeOff(branch, consumable, 10)})

	var conflict *infra.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ожидался конфликт, получено %v", err)
	}
	if quantity(t, repo, branch, consumable) != -1 {
		t.Fatal("позиция не должна была создаться")
	}
}

func TestWriteOffBeyondStockRejected(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 50)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	_, _, err := repo.ApplyMovements(ctx, nil, "", []Movement{writeOff(branch, consumable, 60)})

	var conflict *infra.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ожидался конфликт, получено %v", err)
	}
	if got := quantity(t, repo, branch, consumable); got != 50 {
		t.Fatalf("остаток изменился при неудачном списании: %v", got)
	}
}

func TestWriteOffToZeroAllowed(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 60)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{writeOff(branch, consumable, 60)},
	); err != nil {
		t.Fatalf("списание в ноль должно проходить: %v", err)
	}
	if got := quantity(t, repo, branch, consumable); got != 0 {
		t.Fatalf("остаток %v вместо нуля", got)
	}
}

// Частично списанный набор материалов хуже, чем не списанный вовсе.
func TestPartialFailureRollsBackWholeSet(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch := uuid.New()
	paint, oxidizer := uuid.New(), uuid.New()

	if _, _, err := repo.ApplyMovements(ctx, nil, "", []Movement{
		replenish(branch, paint, 100),
		replenish(branch, oxidizer, 10),
	}); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	// Второй позиции не хватит — первая тоже не должна списаться.
	_, _, err := repo.ApplyMovements(ctx, nil, "", []Movement{
		writeOff(branch, paint, 60),
		writeOff(branch, oxidizer, 90),
	})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	if got := quantity(t, repo, branch, paint); got != 100 {
		t.Fatalf("краска списалась несмотря на откат: %v", got)
	}
}

func TestMovementsAreRecorded(t *testing.T) {
	pool := newPool(t)
	repo, ctx := NewRepository(pool), context.Background()
	branch, consumable := uuid.New(), uuid.New()

	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 100)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{writeOff(branch, consumable, 40)},
	); err != nil {
		t.Fatalf("списание: %v", err)
	}

	var count int
	var sum float64
	err := pool.QueryRow(ctx,
		"SELECT count(*), coalesce(sum(delta), 0) FROM stock_movements WHERE branch_id = $1",
		branch).Scan(&count, &sum)
	if err != nil {
		t.Fatalf("история: %v", err)
	}
	if count != 2 || sum != 60 {
		t.Fatalf("история движений: %d записей, сумма %v", count, sum)
	}
}

// Отметка в inbox пишется той же транзакцией, что и движения. Если списание
// упало, событие не считается обработанным и повтор доставки его отработает.
func TestFailedMovementReleasesInboxMark(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	eventID := uuid.New()

	// Позиции нет — списание упадёт вместе с отметкой.
	if _, _, err := repo.ApplyMovements(
		ctx, &eventID, "appointment.completed", []Movement{writeOff(branch, consumable, 10)},
	); err == nil {
		t.Fatal("ожидалась ошибка")
	}

	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 100)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	// Тот же event_id должен отработать: отметка откатилась вместе с транзакцией.
	_, processed, err := repo.ApplyMovements(
		ctx, &eventID, "appointment.completed", []Movement{writeOff(branch, consumable, 10)},
	)
	if err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if !processed {
		t.Fatal("событие считается обработанным, хотя первая попытка упала")
	}
	if got := quantity(t, repo, branch, consumable); got != 90 {
		t.Fatalf("остаток %v вместо 90", got)
	}
}

func TestDuplicateEventSkipped(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	eventID := uuid.New()
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 100)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	for attempt := range 3 {
		_, processed, err := repo.ApplyMovements(
			ctx, &eventID, "appointment.completed", []Movement{writeOff(branch, consumable, 10)},
		)
		if err != nil {
			t.Fatalf("попытка %d: %v", attempt, err)
		}
		if attempt == 0 && !processed {
			t.Fatal("первая попытка должна пройти")
		}
		if attempt > 0 && processed {
			t.Fatal("повтор не должен считаться новым событием")
		}
	}

	if got := quantity(t, repo, branch, consumable); got != 90 {
		t.Fatalf("остаток %v — списание применилось несколько раз", got)
	}
}

func TestSetThreshold(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()
	branch, consumable := uuid.New(), uuid.New()
	if _, _, err := repo.ApplyMovements(
		ctx, nil, "", []Movement{replenish(branch, consumable, 40)},
	); err != nil {
		t.Fatalf("пополнение: %v", err)
	}

	item, err := repo.SetThreshold(ctx, branch, consumable, 100)
	if err != nil {
		t.Fatalf("порог: %v", err)
	}
	if !item.IsLow() {
		t.Fatal("остаток 40 при пороге 100 должен считаться низким")
	}

	low, err := repo.ListLow(ctx, branch)
	if err != nil {
		t.Fatalf("список ниже порога: %v", err)
	}
	if len(low) != 1 {
		t.Fatalf("ожидалась одна позиция, получено %d", len(low))
	}
}

func TestSetThresholdOnMissingItem(t *testing.T) {
	repo, ctx := NewRepository(newPool(t)), context.Background()

	_, err := repo.SetThreshold(ctx, uuid.New(), uuid.New(), 10)

	var notFound *infra.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("ожидался NotFound, получено %v", err)
	}
}
