// Command schemasql prints the ClickHouse schema defined in
// internal/storage/schema.go. scripts/migrate.sql is its output:
//
//	go run ./cmd/schemasql > scripts/migrate.sql
package main

import (
	"fmt"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

func main() {
	fmt.Print(storage.SchemaSQL())
}
