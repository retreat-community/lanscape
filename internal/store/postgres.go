package store

// postgresDriver is the database/sql driver name used for PostgreSQL DSNs. The server
// binary registers a driver under this name when built with PostgreSQL support
// (see docs/decisions); without it Open reports an unknown driver.
const postgresDriver = "pgx"
