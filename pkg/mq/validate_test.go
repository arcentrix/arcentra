package mq

import "testing"

func TestRequireNonEmpty(t *testing.T) {
	if err := RequireNonEmpty("name", "value"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := RequireNonEmpty("name", ""); err == nil {
		t.Fatal("expected error for empty value")
	}
}

func TestRequireNonEmptySlice(t *testing.T) {
	if err := RequireNonEmptySlice("items", []string{"a"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := RequireNonEmptySlice("items", nil); err == nil {
		t.Fatal("expected error for nil slice")
	}
	if err := RequireNonEmptySlice("items", []string{}); err == nil {
		t.Fatal("expected error for empty slice")
	}
}
