package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"

	"github.com/spboyer/waza/internal/jsonrpc"
	"github.com/spboyer/waza/internal/webserver"
	"github.com/spf13/cobra"
)

func newServeCommand() *cobra.Command {
	var tcpAddr string
	var tcpAllowRemote bool
	var httpMode bool
	var httpPort int
	var noBrowser bool
	var resultsDir string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the waza server (HTTP dashboard or JSON-RPC)",
		Long: `Start the waza server.

By default, an HTTP server is started that serves the waza dashboard and API.
The browser is opened automatically (disable with --no-browser).

Use --tcp to start a JSON-RPC 2.0 server instead (for IDE integration).
TCP defaults to loopback (127.0.0.1) for security. Use --tcp-allow-remote to bind
to all interfaces.

JSON-RPC methods (when using --tcp or stdin/stdout):
  eval.run       Run an eval (returns run ID)
  eval.list      List available evals in a directory
  eval.get       Get eval details
  eval.validate  Validate an eval spec
  task.list      List tasks for an eval
  task.get       Get task details
  run.status     Get run status
  run.cancel     Cancel a running eval`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.Default()

			// JSON-RPC TCP mode
			if tcpAddr != "" {
				return runJSONRPC(tcpAddr, tcpAllowRemote, logger)
			}

			// HTTP mode (default)
			if httpMode || !cmd.Flags().Changed("tcp") {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer stop()

				srv := webserver.New(webserver.Config{
					Port:       httpPort,
					ResultsDir: resultsDir,
					NoBrowser:  noBrowser,
					Logger:     logger,
				})
				return srv.ListenAndServe(ctx)
			}

			// Fallback: JSON-RPC on stdio
			return runJSONRPCStdio(logger)
		},
	}

	cmd.Flags().StringVar(&tcpAddr, "tcp", "", "TCP address to listen on for JSON-RPC (e.g., :9000)")
	cmd.Flags().BoolVar(&tcpAllowRemote, "tcp-allow-remote", false,
		"Allow binding to non-loopback addresses (WARNING: exposes the server to the network with no authentication)")
	cmd.Flags().BoolVar(&httpMode, "http", false, "Start HTTP dashboard server (default when --tcp is not set)")
	cmd.Flags().IntVar(&httpPort, "port", 3000, "HTTP server port")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Don't auto-open the browser")
	cmd.Flags().StringVar(&resultsDir, "results-dir", ".", "Directory to read results from")

	return cmd
}

func runJSONRPC(tcpAddr string, allowRemote bool, logger *slog.Logger) error {
	registry := jsonrpc.NewMethodRegistry()
	hctx := jsonrpc.NewHandlerContext()
	jsonrpc.RegisterHandlers(registry, hctx)

	server := jsonrpc.NewServer(registry, logger)

	tcpAddr = resolveTCPAddr(tcpAddr, allowRemote, logger)
	listener, err := jsonrpc.NewTCPListener(tcpAddr, server)
	if err != nil {
		return fmt.Errorf("failed to start TCP server: %w", err)
	}
	defer listener.Close() //nolint:errcheck
	fmt.Fprintf(os.Stderr, "JSON-RPC server listening on %s\n", listener.Addr())
	return listener.Serve()
}

func runJSONRPCStdio(logger *slog.Logger) error {
	registry := jsonrpc.NewMethodRegistry()
	hctx := jsonrpc.NewHandlerContext()
	jsonrpc.RegisterHandlers(registry, hctx)

	server := jsonrpc.NewServer(registry, logger)
	fmt.Fprintln(os.Stderr, "JSON-RPC server running on stdio")
	server.ServeStdio(os.Stdin, os.Stdout)
	return nil
}

// resolveTCPAddr ensures TCP addresses default to loopback unless --tcp-allow-remote is set.
func resolveTCPAddr(addr string, allowRemote bool, logger *slog.Logger) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Likely just a port like "9000"; treat as ":9000".
		host = ""
		port = addr
	}

	if allowRemote {
		logger.Warn("TCP server binding to all interfaces — no authentication is provided",
			"address", addr)
		return addr
	}

	// Default to loopback if no host specified or if 0.0.0.0/:: is used without --tcp-allow-remote.
	if host == "" || host == "0.0.0.0" || host == "::" {
		logger.Info("JSON-RPC server listening on TCP (local only)")
		return net.JoinHostPort("127.0.0.1", port)
	}

	return addr
}
