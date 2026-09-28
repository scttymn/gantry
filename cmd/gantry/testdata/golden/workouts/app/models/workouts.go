package models

// Errors are the Rails model's validations, as its full messages.
func (w Workout) Errors() []string {
	var errs []string
	if !w.Date.Valid {
		errs = append(errs, "Date can't be blank")
	}
	return errs
}
