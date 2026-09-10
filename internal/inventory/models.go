package inventory

import (
	"time"

	"github.com/google/uuid"
)

// StockItem — остаток материала на складе филиала.
// Справочником материалов владеет catalog-service, здесь только количества.
type StockItem struct {
	BranchID     uuid.UUID
	ConsumableID uuid.UUID
	Name         string
	Unit         string
	Quantity     float64
	Threshold    float64
	UpdatedAt    time.Time
}

func (s StockItem) IsLow() bool { return s.Quantity < s.Threshold }

// Movement — одно движение по складу. Списание отрицательное, пополнение
// положительное: история восстанавливается суммированием.
type Movement struct {
	BranchID      uuid.UUID
	ConsumableID  uuid.UUID
	Name          string
	Unit          string
	Delta         float64
	Reason        string
	AppointmentID *uuid.UUID
}

const (
	ReasonWriteOff  = "write_off"
	ReasonReplenish = "replenish"
)
