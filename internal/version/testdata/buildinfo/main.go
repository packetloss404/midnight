package main

import (
	"encoding/json"
	"os"

	"github.com/midnight/midnight/internal/version"
)

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(version.Current()); err != nil {
		panic(err)
	}
}
