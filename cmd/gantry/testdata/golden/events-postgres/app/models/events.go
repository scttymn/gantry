package models

import "strings"

// Errors are the Rails model's validations, as its full messages.
func (e Event) Errors() []string {
	var errs []string
	if strings.TrimSpace(e.Name) == "" {
		errs = append(errs, "Name can't be blank")
	}
	return errs
}
