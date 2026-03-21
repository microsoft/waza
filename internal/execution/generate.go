package execution

//go:generate go tool mockgen -package execution -destination copilot_client_wrapper_mocks_test.go . CopilotSession,CopilotClient
//go:generate go tool mockgen -package execution -destination execution_mocks.go . GitResource
