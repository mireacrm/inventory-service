package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/mireacrm/inventory-service/internal/inventory"
)

type ReplenishItem struct {
	ConsumableID uuid.UUID `json:"consumable_id"`
	Name         string    `json:"name"`
	Unit         string    `json:"unit"`
	Amount       float64   `json:"amount"`
}

type ReplenishRequest struct {
	Items []ReplenishItem `json:"items"`
}

type ThresholdRequest struct {
	Threshold float64 `json:"threshold"`
}

type StockOut struct {
	ConsumableID uuid.UUID `json:"consumable_id"`
	Name         string    `json:"name"`
	Unit         string    `json:"unit"`
	Quantity     float64   `json:"quantity"`
	Threshold    float64   `json:"threshold"`
	IsLow        bool      `json:"is_low"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func itemOut(item inventory.StockItem) StockOut {
	return StockOut{
		ConsumableID: item.ConsumableID,
		Name:         item.Name,
		Unit:         item.Unit,
		Quantity:     item.Quantity,
		Threshold:    item.Threshold,
		IsLow:        item.IsLow(),
		UpdatedAt:    item.UpdatedAt,
	}
}

func stockOut(items []inventory.StockItem) []StockOut {
	out := make([]StockOut, 0, len(items))
	for _, item := range items {
		out = append(out, itemOut(item))
	}
	return out
}
