package devcheck

import (
	"path"
	"regexp"
)

var gooseDown = regexp.MustCompile(`(?m)^-- \+goose Down[ \t\r]*$`)

func checkMigrationDown(s *Source, report func(int, string)) error {
	if path.Dir(s.Path) == "db/migrations" && !gooseDown.MatchString(s.Text) {
		report(0, "")
	}
	return nil
}
