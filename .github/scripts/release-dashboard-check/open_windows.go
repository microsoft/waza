package main

import "os"

func openFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
