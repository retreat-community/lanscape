package store

import _ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver, registered as "pgx"

// postgresDriver is the database/sql driver name used for PostgreSQL DSNs.
const postgresDriver = "pgx"
