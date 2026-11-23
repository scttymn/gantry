package models

import "strings"

// Errors are the Rails model's validations, as its full messages.
func (p Post) Errors() []string {
	var errs []string
	if strings.TrimSpace(p.Title) == "" {
		errs = append(errs, "Title can't be blank")
	}
	return errs
}
