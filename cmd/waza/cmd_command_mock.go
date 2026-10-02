package main

import (
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/spf13/cobra"
)

func newCommandMockCommand() *cobra.Command {
	var root string
	var name string
	cmd := &cobra.Command{
		Use:    "__command-mock",
		Short:  "Run an internal command mock",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := commandmock.RunCommand(root, name, args)
			if code != 0 {
				return &ExitCodeError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "private command mock runtime directory")
	cmd.Flags().StringVar(&name, "name", "", "mocked executable name")
	_ = cmd.MarkFlagRequired("root")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
