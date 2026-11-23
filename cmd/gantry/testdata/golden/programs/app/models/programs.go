package models

import "strings"

// Errors are the Rails model's validations, as its full messages.
func (p Program) Errors() []string {
	var errs []string
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, "Name can't be blank")
	}
	return errs
}
