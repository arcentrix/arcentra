package main

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  greeting
	}{
		{"arcentra", greeting{"HELLO, ARCENTRA!", 8}},
		{"  Agent\t", greeting{"HELLO, AGENT!", 5}},
		{"z", greeting{"HELLO, Z!", 1}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalize(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("normalize(%q) = %+v, %v; want %+v", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{"", " \n", "agent1", "hello-world", "代理", strings.Repeat("a", 33)} {
		t.Run(input, func(t *testing.T) {
			if _, err := normalize(input); err == nil {
				t.Fatalf("normalize(%q) accepted invalid input", input)
			}
		})
	}
}
