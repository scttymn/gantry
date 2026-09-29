package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON is a column holding T as JSON (Rails' json columns): jsonb on
// Postgres, text on SQLite, the same Go on both. It's encoded when written
// and decoded when read, and NULL reads as T's zero value. Shaped as
// sql.Null[T]: the value is V. For sqlc, name it in the model's package,
// and point the column at it in sqlc.yaml's overrides:
//
//	type Settings = db.JSON[SiteSettings]
//
//	overrides:
//	  - column: sites.settings
//	    go_type: { type: Settings }
type JSON[T any] struct {
	V T
}

// Value is V as JSON, for the database.
func (j JSON[T]) Value() (driver.Value, error) {
	b, err := json.Marshal(j.V)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan reads the column into V.
func (j *JSON[T]) Scan(src any) error {
	var zero T
	j.V = zero
	switch v := src.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(v, &j.V)
	case string:
		return json.Unmarshal([]byte(v), &j.V)
	}
	return fmt.Errorf("db.JSON: can't read %T", src)
}
