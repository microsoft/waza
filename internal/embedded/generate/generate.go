// Generates all the proper copilot CLI SDK bundles, so we can use them in waza.
// The .zst, .license and generated .go files should all be checked in. When waza is built
// only the relevant copilot CLI package will be added.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

//go:generate go run . windows/arm64 windows/amd64 linux/arm64 linux/amd64 darwin/arm64 darwin/amd64

func main() {
	g := errgroup.Group{}

	platforms := os.Args[1:]

	fmt.Printf("Generating the following platforms:\n")

	for _, p := range platforms {
		fmt.Println(p)
	}

	fmt.Println("Starting...")

	outputDir := ".."

	for _, arg := range platforms {
		g.Go(func() error {
			cmd := exec.Command("go", "tool", "bundler", "-platform", arg, "-output", outputDir)

			fmt.Printf("Running %s\n", cmd.String())

			output, err := cmd.CombinedOutput()

			if err != nil {
				return fmt.Errorf("bundler failed (with output %s): %w", string(output), err)
			}

			// once it's finished we just need to slap the proper package name on it.
			platformParts := strings.Split(arg, "/")

			if len(platformParts) != 2 {
				return fmt.Errorf("bad format for platform %q. Platforms should be <GOOS compatible OS>/<GOARCH compatible arch> (ex: windows/amd64 )", arg)
			}

			path := filepath.Join(outputDir, fmt.Sprintf("zcopilot_%s_%s.go", platformParts[0], platformParts[1]))

			fmt.Printf("Patching %s's package directive\n", path)
			return fixCopilotPackageInGoFile(path)
		})
	}

	if err := g.Wait(); err != nil {
		fmt.Printf("Failed to generate bundles: %s\n", err)
		os.Exit(1)
	} else {
		fmt.Println("Done, no errors")
		fmt.Println("You must delete any older .zst or .license files, manually")
	}
}

func fixCopilotPackageInGoFile(goFile string) error {
	contents, err := os.ReadFile(goFile)

	if err != nil {
		return fmt.Errorf("failed to read %q to fix the package: %w", goFile, err)
	}

	contents = bytes.Replace(contents, []byte("package main"), []byte("package embedded"), 1)

	err = os.WriteFile(goFile, contents, 0644)

	if err != nil {
		return fmt.Errorf("failed to rewrite %q to fix the package: %w", goFile, err)
	}

	cmd := exec.Command("gofmt", "-w", goFile)
	stdout, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to gofmt %q. output: %s: %w", goFile, stdout, err)
	}

	return nil
}
