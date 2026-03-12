package main

//go:generate go tool mockgen -package main -destination copilot_client_wrapper_mocks_test.go github.com/microsoft/waza/internal/execution CopilotSession,CopilotClient
