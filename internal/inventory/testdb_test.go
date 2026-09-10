//go:build integration

package inventory

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mireacrm/go-common/infra"
	"github.com/mireacrm/inventory-service/migrations"
)

// Интеграционные тесты вынесены под тег `integration`: `go test ./...` без базы
// остаётся быстрым, а в CI база поднимается отдельным шагом.
//
//	go test -tags=integration ./...

const defaultDSN = "postgres://inventory_user:inventory_pass@localhost:5432/inventory_db_test"

func testDSN() string {
	if dsn := os.Getenv("INVENTORY_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return defaultDSN
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	if err := infra.Migrate(ctx, testDSN(), migrations.Files); err != nil {
		t.Fatalf("миграции: %v", err)
	}

	pool, err := infra.NewPool(ctx, testDSN())
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, table := range []string{"stock_movements", "stock_items", "processed_events"} {
		if _, err := pool.Exec(ctx, "TRUNCATE "+table+" CASCADE"); err != nil {
			t.Fatalf("очистка %s: %v", table, err)
		}
	}
	return pool
}

func replenish(branch, consumable uuid.UUID, amount float64) Movement {
	return Movement{
		BranchID: branch, ConsumableID: consumable,
		Name: "Краска 6.0", Unit: "г", Delta: amount, Reason: ReasonReplenish,
	}
}

func writeOff(branch, consumable uuid.UUID, amount float64) Movement {
	return Movement{
		BranchID: branch, ConsumableID: consumable,
		Name: "Краска 6.0", Unit: "г", Delta: -amount, Reason: ReasonWriteOff,
	}
}
