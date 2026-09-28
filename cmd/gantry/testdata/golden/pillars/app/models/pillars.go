package models

// Errors are the Rails model's validations, as its full messages.
func (p Pillar) Errors() []string {
	var errs []string
	errs = append(errs, Required("Title", p.Title)...)
	return errs
}
