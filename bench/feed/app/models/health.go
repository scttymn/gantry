package models

import "context"

// Ping is whether the database answers, for the health check: a query on
// no table.
func (q *Queries) Ping(ctx context.Context) error {
	var one int
	return q.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}
