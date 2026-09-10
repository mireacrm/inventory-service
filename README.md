# Сервис расходников

Складские остатки по филиалам: списание материалов по нормативам при
завершении визита, приход, инвентаризация. Потребитель доменных событий —
на завершённый визит списывает расход сам, синхронно его никто не дёргает.

## Зависимости

| Модуль | Роль |
|---|---|
| [`mireacrm/contracts-go`](https://github.com/mireacrm/contracts-go) | сообщения и стабы gRPC |
| [`mireacrm/go-common`](https://github.com/mireacrm/go-common) | транспорт, трассировка, метрики, каркас процесса |

## Локально

```
go test ./...                    # модульные
go test -tags integration ./...  # нужен Postgres
docker build -t inventory-service .
```

Систему целиком поднимает [`mireacrm/deploy`](https://github.com/mireacrm/deploy).
