package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
// protocol. The database name is quoted, since MotherDuck database names may
// contain characters (such as hyphens) that are not valid bare identifiers.
const motherDuckAttach = "ATTACH 'md:%s' AS \"%s\";"

// motherDuckCreateTable creates the token usage table in the target MotherDuck
// database if it does not yet exist, with the same schema as the local usage
// table but without any rows, keyed on the response id.
const motherDuckCreateTable = `
	CREATE TABLE IF NOT EXISTS "%s".token_usage (
		timestamp TIMESTAMP,
		response_id VARCHAR PRIMARY KEY,
		status VARCHAR,
		prompt_tokens BIGINT,
		completion_tokens BIGINT,
		total_tokens BIGINT,
		cached_tokens BIGINT,
		session_id VARCHAR,
		session_name VARCHAR,
		request_received_at TIMESTAMP,
		first_response_at TIMESTAMP,
		last_response_at TIMESTAMP,
		error_at TIMESTAMP,
		first_response_latency_ms BIGINT,
		total_response_latency_ms BIGINT
	);
`

// motherDuckInsert inserts the rows of the local token usage table that are
// not yet present in the target MotherDuck token usage table into the target
// table, skipping rows whose response_id already exists there, so repeated
// uploads of the same accumulated usage do not duplicate data.
const motherDuckInsert = `
	INSERT INTO "%s".token_usage
	SELECT * FROM token_usage AS l
	WHERE NOT EXISTS (
		SELECT 1 FROM "%s".token_usage AS t
		WHERE t.response_id = l.response_id
	);
`

// MotherDuckDatabase returns the target MotherDuck database name for the usage
// upload, or an empty string when the upload is disabled.
func MotherDuckDatabase() string {
	return os.Getenv(motherDuckDatabaseEnv)
}

// uploadToMotherDuck uploads the token usage accumulated in the local usage
// database to MotherDuck over the `md:` protocol, using the DuckDB driver. It
// is a no-op when no target database is configured and returns an error if the
// upload fails, so callers can log and continue shutdown. The local usage
// database must not be held open by the caller while this runs, since the
// database/sql pool connection cannot be shared with the duckdb cli in that
// way and the usage database is closed before the upload runs.
func (s *tokenCounterRequestProcessor) uploadToMotherDuck(ctx context.Context, dbPath string) error {
	if s.mdDatabase == "" {
		s.logger.Debug("no motherduck database configured, skipping usage upload")
		return nil
	}
	s.logger.Info("uploading token usage to motherduck",
		slog.String("database", s.mdDatabase),
		slog.String("db_path", dbPath),
	)
	conn, err := s.store.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire usage database connection for motherduck upload: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, motherDuckInstall); err != nil {
		return fmt.Errorf("failed to install the motherduck extension: %w", err)
	}
	if _, err := conn.ExecContext(ctx, motherDuckLoad); err != nil {
		return fmt.Errorf("failed to load the motherduck extension: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckAttach, s.mdDatabase, s.mdDatabase)); err != nil {
		return fmt.Errorf("failed to attach to motherduck via the md: protocol: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckCreateTable, s.mdDatabase)); err != nil {
		return fmt.Errorf("failed to create motherduck token usage table: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckInsert, s.mdDatabase, s.mdDatabase)); err != nil {
		return fmt.Errorf("failed to insert token usage into motherduck: %w", err)
	}
	return nil
}
