// This synthetic acceptance program uses only the Go standard library.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type greeting struct {
	Message string `json:"message"`
	Length  int    `json:"length"`
}

func normalize(name string) (greeting, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 32 {
		return greeting{}, fmt.Errorf("name must contain 1 to 32 ASCII letters")
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') {
			return greeting{}, fmt.Errorf("name must contain only ASCII letters")
		}
	}
	return greeting{Message: "HELLO, " + strings.ToUpper(name) + "!", Length: len(name)}, nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: stdlib-demo NAME")
		os.Exit(2)
	}
	value, err := normalize(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
