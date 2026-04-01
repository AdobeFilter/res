module valhalla/control-plane

go 1.22

require (
	valhalla/common v0.0.0
	github.com/jackc/pgx/v5 v5.5.5
	github.com/golang-jwt/jwt/v5 v5.2.1
	go.uber.org/zap v1.27.0
	golang.org/x/crypto v0.21.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20231201235250-de7065d80cb9 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)

replace valhalla/common => ../valhalla-common
