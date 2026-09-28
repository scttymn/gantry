package models

// Errors are the Rails model's validations, as its full messages.
func (p Program) Errors() []string {
	var errs []string
	errs = append(errs, Required("Name", p.Name)...)
	return errs
}
