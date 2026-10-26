// Command mdinstall installs the MotherDuck (md) DuckDB extension into the
// DuckDB extension directory used by the embedded runtime. It is meant to be run
// during image build so that the processor does not have to download the
// extension over the network when it installs and loads it on shutdown.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "github.com/duckdb/duckdb-go/v2"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open duckdb database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "INSTALL md;"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to install the motherduck extension: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("installed motherduck extension")
}
