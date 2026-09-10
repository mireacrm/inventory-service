package config

import "github.com/mireacrm/go-common/infra"

type Config struct {
	ServiceName  string
	PostgresDSN  string
	HTTPPort     int
	GRPCPort     int
	AMQPURL      string
	NATSURL      string
	CatalogAddr  string
	OTLPEndpoint string
	Debug        bool
}

func Load() (Config, error) {
	cfg := Config{
		ServiceName: "inventory-service",
		PostgresDSN: infra.Env("INVENTORY_POSTGRES_DSN", "postgres://inventory_user:inventory_pass@localhost:5432/inventory_db"),
		AMQPURL:     infra.Env("INVENTORY_AMQP_URL", "amqp://guest:guest@localhost:5672/"),
		NATSURL:     infra.Env("INVENTORY_NATS_URL", "nats://localhost:4222"),
		CatalogAddr: infra.Env("INVENTORY_CATALOG_ADDR", "localhost:9002"),
		// Пустой адрес выключает экспорт трасс.
		OTLPEndpoint: infra.Env("INVENTORY_OTLP_ENDPOINT", ""),
		Debug:        infra.Env("INVENTORY_DEBUG", "false") == "true",
	}

	var err error
	if cfg.HTTPPort, err = infra.EnvInt("INVENTORY_HTTP_PORT", 8004); err != nil {
		return cfg, err
	}
	if cfg.GRPCPort, err = infra.EnvInt("INVENTORY_GRPC_PORT", 9004); err != nil {
		return cfg, err
	}
	return cfg, nil
}
