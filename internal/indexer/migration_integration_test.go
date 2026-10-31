package indexer_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ktechbro/settlement-proof/internal/indexer"
)

func TestPostgresMigrationPreservesLegacyEVMRecords(t *testing.T) {
	base := databaseConfig(t, "http://unused.test")
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, base.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	name := fmt.Sprint("migration_test_", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = connection.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer connection.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE") // Isolated test schema only.
	if _, err = connection.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	_, err = connection.Exec(ctx, `INSERT INTO scopes(scope,chain,config) VALUES('legacy','legacy-evm','{}');
 INSERT INTO checkpoints(scope,height,hash) VALUES('legacy',42,'old-finalized-hash');
 INSERT INTO settlements(chain,asset,tx_hash,log_index,block_number,block_hash,block_time,sender,recipient,amount,provider,finalized_number,finalized_hash,raw)
 VALUES('legacy-evm','old-token','old-tx',1,40,'old-hash',1700000000,'old-sender','old-recipient',50000000,'old-provider',42,'old-finalized-hash','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(base.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	base.DatabaseURL = parsed.String()
	store, err := indexer.OpenStore(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Pool.Close()
	var protocol, amount string
	var outer *int
	if err = store.Pool.QueryRow(ctx, "SELECT protocol,amount::text,outer_instruction FROM settlements WHERE chain='legacy-evm'").Scan(&protocol, &amount, &outer); err != nil {
		t.Fatal(err)
	}
	if protocol != "evm" || amount != "50000000" || outer != nil {
		t.Fatal("legacy settlement changed")
	}
	var height int64
	if err = store.Pool.QueryRow(ctx, "SELECT height FROM checkpoints WHERE scope='legacy'").Scan(&height); err != nil || height != 42 {
		t.Fatal("legacy checkpoint changed", err)
	}
	reopened, err := indexer.OpenStore(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Pool.Close()
	var count int
	if err = reopened.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatal("migration was not applied once", err)
	}
}
