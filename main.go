package main

import (
	"fmt"
	"github.com/ArchieOS-org/sparestep/internal/app"
	"os"
)

func main() {
	if err := app.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Sparestep:", err)
		os.Exit(1)
	}
}
