package main

import (
	"context"
	"fmt"
	"os"
)

// motherDuckDatabaseEnv is the environment variable that names the target
// MotherDuck database that the local usage database is uploaded to when the
// processor shuts down. When unset, the usage upload to MotherDuck is skipped.
const motherDuckDatabaseEnv = "EXTPROC_MOTHERDUCK_DATABASE"

// motherDuckInstallAndLoad installs and loads the MotherDuck extension used to
// speak the `md:` protocol.
const motherDuckInstallAndLoad = "INSTALL AND LOAD md;"

// motherDuckAttach attaches the MotherDuck instance using the `md:` protocol.
const motherDuckAttach = "ATTACH 'md:';"

// motherDuckUploadDatabase creates or replaces a MotherDuck database holding
// the contents of the currently attached database.
const motherDuckUploadDatabase = "CREATE OR REPLACE DATABASE %s FROM CURRENT_DATABASE();"

// MotherDuckDatabase returns the target MotherDuck database name for the usage
// upload, or an empty string when the upload is disabled.
func MotherDuckDatabase() string {
	return os.Getenv(motherDuckDatabaseEnv)
}

// uploadToMotherDuck uploads the token usage accumulated in the local usage
// database to MotherDuck over the `md:` protocol, using the configured target
// MotherDuck database. It is a no-op when no target database is configured and
// returns an error if the upload fails, so callers can log and continue shutdown.
func (s *tokenCounterRequestProcessor) uploadToMotherDuck(ctx context.Context) error {
	if s.mdDatabase == "" {
		s.logger.Debug("no motherduck database configured, skipping usage upload")
		return nil
	}

	conn, err := s.store.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire usage database connection for motherduck upload: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, motherDuckInstallAndLoad); err != nil {
		return fmt.Errorf("failed to install and load the motherduck extension: %w", err)
	}
	if _, err := conn.ExecContext(ctx, motherDuckAttach); err != nil {
		return fmt.Errorf("failed to attach to motherduck via the md: protocol: %w", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(motherDuckUploadDatabase, s.mdDatabase)); err != nil {
		return fmt.Errorf("failed to upload usage database to motherduck database %q: %w", s.mdDatabase, err)
	}
	return nil
}
