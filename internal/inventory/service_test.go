package inventory

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/mireacrm/contracts-go/mirea/events/v1"
	"github.com/mireacrm/go-common/infra"
)

type fakeNorms struct {
	norms []Norm
	err   error
}

func (f fakeNorms) ForService(context.Context, uuid.UUID) ([]Norm, error) {
	return f.norms, f.err
}

type fakeRepo struct {
	applied   [][]Movement
	result    []StockItem
	applyErr  error
	processed map[uuid.UUID]bool
}

func newRepo() *fakeRepo { return &fakeRepo{processed: map[uuid.UUID]bool{}} }

func (f *fakeRepo) ApplyMovements(
	_ context.Context, eventID *uuid.UUID, _ string, movements []Movement,
) ([]StockItem, bool, error) {
	// Фейк повторяет контракт настоящего репозитория: отметка и движения
	// в одной транзакции, при ошибке ничего не остаётся.
	if eventID != nil {
		if f.processed[*eventID] {
			return nil, false, nil
		}
	}
	if f.applyErr != nil {
		return nil, false, f.applyErr
	}
	if eventID != nil {
		f.processed[*eventID] = true
	}
	f.applied = append(f.applied, movements)
	return f.result, true, nil
}

func (f *fakeRepo) List(context.Context, uuid.UUID) ([]StockItem, error)    { return f.result, nil }
func (f *fakeRepo) ListLow(context.Context, uuid.UUID) ([]StockItem, error) { return f.result, nil }

func (f *fakeRepo) SetThreshold(
	context.Context, uuid.UUID, uuid.UUID, float64,
) (StockItem, error) {
	return StockItem{}, nil
}

type fakeEvents struct{ keys []string }

func (f *fakeEvents) Publish(_ context.Context, key string, _ *eventsv1.EventEnvelope) error {
	f.keys = append(f.keys, key)
	return nil
}

type fakeRealtime struct{ subjects []string }

func (f *fakeRealtime) Publish(_ context.Context, subject string, _ proto.Message) error {
	f.subjects = append(f.subjects, subject)
	return nil
}

func setup(repo *fakeRepo, norms fakeNorms) (*Service, *fakeEvents, *fakeRealtime) {
	events, realtime := &fakeEvents{}, &fakeRealtime{}
	return New(repo, norms, events, realtime), events, realtime
}

var (
	branch     = uuid.New()
	consumable = uuid.New()
	paint      = Norm{ConsumableID: consumable, Name: "Краска 6.0", Unit: "г", Amount: 60}
)

func TestWriteOffAppliesNorms(t *testing.T) {
	repo := newRepo()
	repo.result = []StockItem{{BranchID: branch, ConsumableID: consumable, Quantity: 500, Threshold: 100}}
	service, events, _ := setup(repo, fakeNorms{norms: []Norm{paint}})

	err := service.WriteOffForAppointment(context.Background(), uuid.New(), uuid.New(), branch, uuid.New())
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(repo.applied) != 1 || len(repo.applied[0]) != 1 {
		t.Fatalf("движения: %v", repo.applied)
	}
	if delta := repo.applied[0][0].Delta; delta != -60 {
		t.Fatalf("списание должно быть отрицательным, получено %v", delta)
	}
	if len(events.keys) != 1 || events.keys[0] != "consumables.written_off" {
		t.Fatalf("события: %v", events.keys)
	}
}

func TestWriteOffIsIdempotent(t *testing.T) {
	// RabbitMQ доставляет at-least-once: повторное событие не должно
	// списать материалы второй раз.
	repo := newRepo()
	repo.result = []StockItem{{BranchID: branch, Quantity: 500, Threshold: 100}}
	service, events, _ := setup(repo, fakeNorms{norms: []Norm{paint}})
	eventID := uuid.New()

	for range 3 {
		if err := service.WriteOffForAppointment(
			context.Background(), eventID, uuid.New(), branch, uuid.New(),
		); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
	}

	if len(repo.applied) != 1 {
		t.Fatalf("списание применено %d раз вместо одного", len(repo.applied))
	}
	if len(events.keys) != 1 {
		t.Fatalf("событий опубликовано %d вместо одного", len(events.keys))
	}
}

func TestLowStockRaisesBothBrokers(t *testing.T) {
	repo := newRepo()
	repo.result = []StockItem{
		{BranchID: branch, ConsumableID: consumable, Name: "Краска 6.0", Unit: "г", Quantity: 40, Threshold: 100},
	}
	service, events, realtime := setup(repo, fakeNorms{norms: []Norm{paint}})

	if err := service.WriteOffForAppointment(
		context.Background(), uuid.New(), uuid.New(), branch, uuid.New(),
	); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(events.keys) != 2 || events.keys[1] != "stock.low" {
		t.Fatalf("ожидались written_off и stock.low, получено %v", events.keys)
	}
	if len(realtime.subjects) != 1 {
		t.Fatalf("алерт в NATS не ушёл: %v", realtime.subjects)
	}
}

func TestLowStockSilentWhenAboveThreshold(t *testing.T) {
	repo := newRepo()
	repo.result = []StockItem{{BranchID: branch, Quantity: 400, Threshold: 100}}
	service, events, realtime := setup(repo, fakeNorms{norms: []Norm{paint}})

	_ = service.WriteOffForAppointment(context.Background(), uuid.New(), uuid.New(), branch, uuid.New())

	if len(events.keys) != 1 {
		t.Fatalf("лишние события: %v", events.keys)
	}
	if len(realtime.subjects) != 0 {
		t.Fatalf("алерт не должен был уйти: %v", realtime.subjects)
	}
}

func TestWriteOffWithoutNormsDoesNothing(t *testing.T) {
	repo := newRepo()
	service, events, _ := setup(repo, fakeNorms{})

	if err := service.WriteOffForAppointment(
		context.Background(), uuid.New(), uuid.New(), branch, uuid.New(),
	); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if len(repo.applied) != 0 || len(events.keys) != 0 {
		t.Fatal("услуга без нормативов не должна трогать склад")
	}
}

func TestFailedWriteOffStaysRetryable(t *testing.T) {
	// Отметка об обработке пишется той же транзакцией, что и списание.
	// Если списание упало, событие не считается обработанным и повтор
	// доставки его отработает.
	repo := newRepo()
	repo.applyErr = infra.Conflict("на складе недостаточно")
	service, _, _ := setup(repo, fakeNorms{norms: []Norm{paint}})
	eventID := uuid.New()

	if err := service.WriteOffForAppointment(
		context.Background(), eventID, uuid.New(), branch, uuid.New(),
	); err == nil {
		t.Fatal("ожидалась ошибка")
	}

	if repo.processed[eventID] {
		t.Fatal("упавшее событие не должно считаться обработанным")
	}

	repo.applyErr = nil
	repo.result = []StockItem{{BranchID: branch, Quantity: 500, Threshold: 100}}
	if err := service.WriteOffForAppointment(
		context.Background(), eventID, uuid.New(), branch, uuid.New(),
	); err != nil {
		t.Fatalf("повтор должен пройти: %v", err)
	}
	if len(repo.applied) != 1 {
		t.Fatalf("повтор не применил списание: %v", repo.applied)
	}
}

func TestCatalogFailureStopsWriteOff(t *testing.T) {
	repo := newRepo()
	service, _, _ := setup(repo, fakeNorms{err: infra.Unavailable("catalog", errors.New("нет связи"))})

	err := service.WriteOffForAppointment(context.Background(), uuid.New(), uuid.New(), branch, uuid.New())

	var unavailable *infra.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("ожидалась недоступность, получено %v", err)
	}
	if len(repo.applied) != 0 {
		t.Fatal("склад не должен меняться, если нормативы неизвестны")
	}
}

func TestReplenishRejectsNonPositive(t *testing.T) {
	service, _, _ := setup(newRepo(), fakeNorms{})

	_, err := service.Replenish(context.Background(), []Movement{{Delta: -5}})

	var invalid *infra.InvalidArgumentError
	if !errors.As(err, &invalid) {
		t.Fatalf("ожидался InvalidArgument, получено %v", err)
	}
}

func TestIsLow(t *testing.T) {
	cases := []struct {
		quantity, threshold float64
		want                bool
	}{
		{40, 100, true},
		{100, 100, false},
		{0, 0, false},
	}
	for _, tc := range cases {
		item := StockItem{Quantity: tc.quantity, Threshold: tc.threshold}
		if item.IsLow() != tc.want {
			t.Fatalf("%v/%v: IsLow=%v", tc.quantity, tc.threshold, item.IsLow())
		}
	}
}
