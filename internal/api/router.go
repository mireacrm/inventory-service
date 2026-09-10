package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mireacrm/go-common/infra"
	"github.com/mireacrm/inventory-service/internal/inventory"
)

type Handler struct {
	service *inventory.Service
}

func NewRouter(
	service *inventory.Service, serviceName string, probes ...infra.Probe,
) http.Handler {
	handler := &Handler{service: service}

	router := chi.NewRouter()
	router.Use(infra.TraceMiddleware, infra.IdentityMiddleware,
		infra.MetricsMiddleware(serviceName))

	router.Get("/branches/{branchID}/stock", handler.list)
	router.Get("/branches/{branchID}/stock/low", handler.listLow)
	router.Post("/branches/{branchID}/stock/replenish", handler.replenish)
	router.Put("/branches/{branchID}/stock/{consumableID}/threshold", handler.setThreshold)

	router.Get("/metrics", infra.MetricsHandler().ServeHTTP)
	router.Get("/healthz", infra.LivenessHandler())
	router.Get("/readyz", infra.ReadinessHandler(probes...))
	return router
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	branchID, err := pathUUID(r, "branchID")
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}

	items, err := h.service.List(r.Context(), branchID)
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}
	infra.WriteJSON(w, http.StatusOK, stockOut(items))
}

func (h *Handler) listLow(w http.ResponseWriter, r *http.Request) {
	branchID, err := pathUUID(r, "branchID")
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}

	items, err := h.service.ListLow(r.Context(), branchID)
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}
	infra.WriteJSON(w, http.StatusOK, stockOut(items))
}

func (h *Handler) replenish(w http.ResponseWriter, r *http.Request) {
	branchID, err := pathUUID(r, "branchID")
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}

	var request ReplenishRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		infra.WriteError(w, r, infra.InvalidArgument("тело запроса: %v", err))
		return
	}
	if len(request.Items) == 0 {
		infra.WriteError(w, r, infra.InvalidArgument("список позиций пуст"))
		return
	}

	movements := make([]inventory.Movement, 0, len(request.Items))
	for _, item := range request.Items {
		movements = append(movements, inventory.Movement{
			BranchID:     branchID,
			ConsumableID: item.ConsumableID,
			Name:         item.Name,
			Unit:         item.Unit,
			Delta:        item.Amount,
		})
	}

	items, err := h.service.Replenish(r.Context(), movements)
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}
	infra.WriteJSON(w, http.StatusOK, stockOut(items))
}

func (h *Handler) setThreshold(w http.ResponseWriter, r *http.Request) {
	branchID, err := pathUUID(r, "branchID")
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}
	consumableID, err := pathUUID(r, "consumableID")
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}

	var request ThresholdRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		infra.WriteError(w, r, infra.InvalidArgument("тело запроса: %v", err))
		return
	}

	item, err := h.service.SetThreshold(r.Context(), branchID, consumableID, request.Threshold)
	if err != nil {
		infra.WriteError(w, r, err)
		return
	}
	infra.WriteJSON(w, http.StatusOK, itemOut(item))
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, infra.InvalidArgument("%s: невалидный UUID", name)
	}
	return id, nil
}
