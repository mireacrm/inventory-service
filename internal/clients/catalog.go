package clients

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	catalogv1 "github.com/mireacrm/contracts-go/mirea/catalog/v1"

	"github.com/mireacrm/go-common/infra"
	"github.com/mireacrm/inventory-service/internal/inventory"
)

type Catalog struct {
	client catalogv1.CatalogServiceClient
	conn   *grpc.ClientConn
}

func DialCatalog(addr string) (*Catalog, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	return &Catalog{client: catalogv1.NewCatalogServiceClient(conn), conn: conn}, nil
}

func (c *Catalog) Close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *Catalog) ForService(
	ctx context.Context, serviceID uuid.UUID,
) ([]inventory.Norm, error) {
	response, err := c.client.GetConsumptionNorms(infra.Outgoing(ctx),
		&catalogv1.GetConsumptionNormsRequest{ServiceId: serviceID.String()})
	if err != nil {
		return nil, translate(err, serviceID)
	}

	norms := make([]inventory.Norm, 0, len(response.GetNorms()))
	for _, item := range response.GetNorms() {
		consumableID, err := uuid.Parse(item.GetConsumableId())
		if err != nil {
			return nil, fmt.Errorf("catalog вернул некорректный consumable_id: %w", err)
		}
		norms = append(norms, inventory.Norm{
			ConsumableID: consumableID,
			Name:         item.GetName(),
			Unit:         item.GetUnit(),
			Amount:       item.GetAmount(),
		})
	}
	return norms, nil
}

func translate(err error, serviceID uuid.UUID) error {
	switch status.Code(err) {
	case codes.PermissionDenied:
		return infra.Forbidden("catalog")
	case codes.NotFound:
		return infra.NotFound("service", serviceID)
	case codes.InvalidArgument:
		return infra.InvalidArgument("%s", status.Convert(err).Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return infra.Unavailable("catalog", err)
	default:
		return err
	}
}
