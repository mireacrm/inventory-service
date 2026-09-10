package inventory

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/mireacrm/contracts-go/mirea/events/v1"
	realtimev1 "github.com/mireacrm/contracts-go/mirea/realtime/v1"

	"github.com/mireacrm/go-common/infra"
)

// Norms — нормативы расхода из catalog-service.
type Norms interface {
	ForService(ctx context.Context, serviceID uuid.UUID) ([]Norm, error)
}

type Norm struct {
	ConsumableID uuid.UUID
	Name         string
	Unit         string
	Amount       float64
}

type Repository interface {
	// ApplyMovements списывает или пополняет позиции одной транзакцией
	// и возвращает получившиеся остатки.
	//
	// Если передан eventID, отметка об обработке пишется той же транзакцией.
	// processed=false означает, что событие уже отрабатывали. Отдельной
	// транзакцией отметку делать нельзя: упавшее списание оставило бы
	// событие помеченным, и повторная доставка молча пропустила бы его.
	ApplyMovements(
		ctx context.Context, eventID *uuid.UUID, routingKey string, movements []Movement,
	) (items []StockItem, processed bool, err error)
	List(ctx context.Context, branchID uuid.UUID) ([]StockItem, error)
	ListLow(ctx context.Context, branchID uuid.UUID) ([]StockItem, error)
	SetThreshold(ctx context.Context, branchID, consumableID uuid.UUID, threshold float64) (StockItem, error)
}

type Events interface {
	Publish(ctx context.Context, routingKey string, envelope *eventsv1.EventEnvelope) error
}

type Realtime interface {
	Publish(ctx context.Context, subject string, message proto.Message) error
}

type Service struct {
	repo     Repository
	norms    Norms
	events   Events
	realtime Realtime
}

func New(repo Repository, norms Norms, events Events, realtime Realtime) *Service {
	return &Service{repo: repo, norms: norms, events: events, realtime: realtime}
}

func alertSubject(branchID uuid.UUID) string {
	return fmt.Sprintf("mirea.branch.%s.alerts", branchID)
}

// WriteOffForAppointment — реакция на appointment.completed.
func (s *Service) WriteOffForAppointment(
	ctx context.Context, eventID, appointmentID, branchID, serviceID uuid.UUID,
) error {
	norms, err := s.norms.ForService(ctx, serviceID)
	if err != nil {
		return err
	}
	if len(norms) == 0 {
		return nil
	}

	movements := make([]Movement, 0, len(norms))
	for _, norm := range norms {
		movements = append(movements, Movement{
			BranchID:      branchID,
			ConsumableID:  norm.ConsumableID,
			Name:          norm.Name,
			Unit:          norm.Unit,
			Delta:         -norm.Amount,
			Reason:        ReasonWriteOff,
			AppointmentID: &appointmentID,
		})
	}

	items, processed, err := s.repo.ApplyMovements(ctx, &eventID, "appointment.completed", movements)
	if err != nil {
		return err
	}
	if !processed {
		slog.InfoContext(ctx, "событие уже обработано, списание пропущено", "event_id", eventID)
		return nil
	}

	s.publishWrittenOff(ctx, appointmentID, branchID, movements)
	s.raiseLowStock(ctx, items)
	return nil
}

func (s *Service) Replenish(ctx context.Context, movements []Movement) ([]StockItem, error) {
	for i := range movements {
		if movements[i].Delta <= 0 {
			return nil, infra.InvalidArgument("количество пополнения должно быть положительным")
		}
		movements[i].Reason = ReasonReplenish
	}
	items, _, err := s.repo.ApplyMovements(ctx, nil, "", movements)
	return items, err
}

func (s *Service) List(ctx context.Context, branchID uuid.UUID) ([]StockItem, error) {
	return s.repo.List(ctx, branchID)
}

func (s *Service) ListLow(ctx context.Context, branchID uuid.UUID) ([]StockItem, error) {
	return s.repo.ListLow(ctx, branchID)
}

func (s *Service) SetThreshold(
	ctx context.Context, branchID, consumableID uuid.UUID, threshold float64,
) (StockItem, error) {
	if threshold < 0 {
		return StockItem{}, infra.InvalidArgument("порог не может быть отрицательным")
	}
	return s.repo.SetThreshold(ctx, branchID, consumableID, threshold)
}

func (s *Service) publishWrittenOff(
	ctx context.Context, appointmentID, branchID uuid.UUID, movements []Movement,
) {
	items := make([]*eventsv1.ConsumablesWrittenOff_Item, 0, len(movements))
	for _, movement := range movements {
		items = append(items, &eventsv1.ConsumablesWrittenOff_Item{
			ConsumableId: movement.ConsumableID.String(),
			Unit:         movement.Unit,
			Amount:       -movement.Delta,
		})
	}

	envelope := &eventsv1.EventEnvelope{
		Payload: &eventsv1.EventEnvelope_ConsumablesWrittenOff{
			ConsumablesWrittenOff: &eventsv1.ConsumablesWrittenOff{
				AppointmentId: appointmentID.String(),
				BranchId:      branchID.String(),
				Items:         items,
			},
		},
	}
	if err := s.events.Publish(ctx, "consumables.written_off", envelope); err != nil {
		slog.ErrorContext(ctx, "событие не опубликовано",
			"routing_key", "consumables.written_off", "error", err)
	}
}

// raiseLowStock шлёт один и тот же факт в оба брокера по-разному: в RabbitMQ
// как доменное событие для отчётов, в NATS как мгновенный алерт на экран.
func (s *Service) raiseLowStock(ctx context.Context, items []StockItem) {
	for _, item := range items {
		if !item.IsLow() {
			continue
		}

		envelope := &eventsv1.EventEnvelope{
			Payload: &eventsv1.EventEnvelope_StockLow{
				StockLow: &eventsv1.StockLow{
					ConsumableId: item.ConsumableID.String(),
					BranchId:     item.BranchID.String(),
					Name:         item.Name,
					Unit:         item.Unit,
					Remaining:    item.Quantity,
					Threshold:    item.Threshold,
				},
			},
		}
		if err := s.events.Publish(ctx, "stock.low", envelope); err != nil {
			slog.ErrorContext(ctx, "событие не опубликовано",
				"routing_key", "stock.low", "error", err)
		}

		alert := &realtimev1.BranchAlert{
			BranchId: item.BranchID.String(),
			Severity: realtimev1.BranchAlert_SEVERITY_WARNING,
			Title:    "Материал заканчивается",
			Body: fmt.Sprintf("%s: осталось %.1f %s при пороге %.1f",
				item.Name, item.Quantity, item.Unit, item.Threshold),
			RaisedAt: timestamppb.Now(),
		}
		if err := s.realtime.Publish(ctx, alertSubject(item.BranchID), alert); err != nil {
			slog.WarnContext(ctx, "алерт не отправлен", "error", err)
		}
	}
}
