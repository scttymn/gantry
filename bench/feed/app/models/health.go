package models

import "context"

func (q *Queries) Ping(ctx context.Context) error {
	var one int
	return q.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}
