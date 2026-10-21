// Command warrant-mcp is Warrant's MCP server for shopping agents (stdio).
//
// Configure your agent's MCP client to run:
//
//	warrant-mcp
//
// with WARRANT_API_URL (default http://localhost:8080) and WARRANT_AGENT_TOKEN
// set. The agent gets two tools, evaluate and checkout, and no PayPal access.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/okemwag/warrant/bridge/mcp"
	"github.com/okemwag/warrant/internal/config"
)

func main() {
	_ = config.LoadDotEnv(".env")
	token := os.Getenv("WARRANT_AGENT_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "warrant-mcp: WARRANT_AGENT_TOKEN is required")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	backend := mcp.NewAPIBackend(config.Get("WARRANT_API_URL", "http://localhost:8080"), token)
	if err := mcp.NewServer(backend, "0.3.0").Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "warrant-mcp:", err)
		os.Exit(1)
	}
}
