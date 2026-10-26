package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// motherDuckDatabaseEnv is the environment variable that names the target
// MotherDuck database that the local usage database is uploaded to when the
// processor shuts down. When unset, the usage upload to MotherDuck is skipped.
const motherDuckDatabaseEnv = "EXTPROC_MOTHERDUCK_DATABASE"

// motherDuckInstall installs the MotherDuck extension used to speak the `md:`
// protocol.
const motherDuckInstall = "INSTALL md;"

// motherDuckLoad loads the MotherDuck extension into the current session.
const motherDuckLoad = "LOAD md;"

// motherDuckAttach attaches the target MotherDuck database using the `md:`
// protocol.
const motherDuckAttach = "ATTACH 'md:%s' AS %s;"

// motherDuckUploadTable creates or replaces the token usage table in the
// target MotherDuck database from the token usage table in the local usage
// database.
const motherDuckUploadTable = "CREATE OR REPLACE TABLE %s.token_usage AS SELECT * FROM token_usage;"

// MotherDuckDatabase returns the target MotherDuck database name for the usage
// upload, or an empty string when the upload is disabled.
func MotherDuckDatabase() string {
	return os.Getenv(motherDuckDatabaseEnv)
}

// uploadToMotherDuck uploads the token usage accumulated in the local usage
// database to MotherDuck over the `md:` protocol, using the DuckDB CLI which
// runs a DuckDB version supported by the MotherDuck extension. It is a no-op
// when no target database is configured and returns an error if the upload
// fails, so callers can log and continue shutdown. The local usage database
// must not be held open by the caller while this runs, since the DuckDB CLI
// cannot open a database file that another process holds a lock on.
func (s *tokenCounterRequestProcessor) uploadToMotherDuck(ctx context.Context, dbPath string) error {
	if s.mdDatabase == "" {
		s.logger.Debug("no motherduck database configured, skipping usage upload")
		return nil
	}
	s.logger.Info("uploading token usage to motherduck",
		slog.String("database", s.mdDatabase),
		slog.String("db_path", dbPath),
	)
	statements := []string{
		motherDuckInstall,
		motherDuckLoad,
		fmt.Sprintf(motherDuckAttach, s.mdDatabase, s.mdDatabase),
		fmt.Sprintf(motherDuckUploadTable, s.mdDatabase),
	}
	for _, stmt := range statements {
		cmd := exec.CommandContext(ctx, "duckdb", dbPath, "-c", stmt)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("duckdb %q failed: %w: %s", stmt, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
