package main

import (
	"fmt"
	"log"
	"os"

	"github.com/microsoft/waza/internal/execution"
)

// Only inspect local definitions; never construct or initialize an engine.
func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: skill-discovery <directory> <declared-name>")
	}
	if !execution.IsSkillAvailable([]string{os.Args[1]}, os.Args[2]) {
		log.Fatal("declared skill was not found in the supplied directory")
	}
	if _, err := fmt.Fprintln(os.Stdout, "available"); err != nil {
		log.Fatal(err)
	}
}
