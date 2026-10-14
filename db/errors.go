package db

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLite's extended result codes for a refused write (sqlite3.h): the same
// numbers from either build's driver.
const (
	sqliteConstraintForeignKey = 787
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// IsUnique reports whether err is a write refused by a unique index or
// primary key, on either engine (Rails' RecordNotUnique): a name that's
// taken, or a partial unique index used as a lock that someone else holds.
func IsUnique(err error) bool {
	return sqliteIs(err, sqliteConstraintUnique, sqliteConstraintPrimaryKey) || pgCode(err, "23505")
}

// IsForeignKey reports whether err is a write refused by a foreign key
// (Rails' InvalidForeignKey): a row that points at one that isn't there, or
// a delete of one others point at.
func IsForeignKey(err error) bool {
	return sqliteIs(err, sqliteConstraintForeignKey) || pgCode(err, "23503")
}

func sqliteIs(err error, codes ...int) bool {
	code, ok := sqliteCode(err)
	for _, c := range codes {
		if ok && code == c {
			return true
		}
	}
	return false
}

func pgCode(err error, code string) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == code
}

// ErrStale is what a model returns when a compare-and-swap changed nothing
// (sqlc's :execrows said 0): someone else changed the row first (Rails'
// StaleObjectError). The router answers it 409 Conflict.
var ErrStale error = staleError{}

type staleError struct{}

func (staleError) Error() string { return "db: the row changed since it was read" }

// HTTPStatus is the status the router answers it with.
func (staleError) HTTPStatus() int { return 409 }
