package main

import (
	"os"

	"github.com/postalsys/mutiauk/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
