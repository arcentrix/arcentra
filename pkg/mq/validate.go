package mq

import "fmt"

// RequireNonEmpty validates a string value is provided.
func RequireNonEmpty(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// RequireNonEmptySlice validates a slice contains at least one item.
func RequireNonEmptySlice(name string, value []string) error {
	if len(value) == 0 {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
