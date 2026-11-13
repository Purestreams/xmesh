package controller

import (
	"fmt"
	"net/http"
)

// Older clients omit enabled and retain toggle behavior. Panel forms submit an
// explicit target so stale pages and retries cannot reverse the intended action.
func optionalEnabledState(r *http.Request) (*bool, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("invalid form")
	}
	values, present := r.PostForm["enabled"]
	if !present {
		return nil, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return nil, fmt.Errorf("choose enable or disable")
	}
	enabled := values[0] == "true"
	return &enabled, nil
}

func desiredEnabledState(current bool, requested *bool) bool {
	if requested != nil {
		return *requested
	}
	return !current
}
