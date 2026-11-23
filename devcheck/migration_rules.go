package devcheck

import (
	"path"
	"regexp"
)

// gooseDown is the line goose starts a migration's down section with.
var gooseDown = regexp.MustCompile(`(?m)^-- \+goose Down[ \t\r]*$`)

func checkMigrationDown(s *Source, report func(int, string)) error {
	if path.Dir(s.Path) == "db/migrations" && !gooseDown.MatchString(s.Text) {
		report(0, "")
	}
	return nil
}
