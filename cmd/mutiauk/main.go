package main

import (
	"os"

	"github.com/Muti-Metroo/Mutiauk/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
