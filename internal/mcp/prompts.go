package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts installs curated ops-workflow templates. Prompts carry no data
// themselves — they steer the model toward the right (RBAC-gated) tools — so
// they need no section gate of their own beyond the bearer auth on /mcp.
func (h *handler) registerPrompts(s *mcpsdk.Server) {
	s.AddPrompt(&mcpsdk.Prompt{
		Name:        "diagnose_container",
		Description: "Investigate why a container is unhealthy or misbehaving.",
		Arguments: []*mcpsdk.PromptArgument{
			{Name: "container", Description: "container ID or name", Required: true},
		},
	}, promptText(func(args map[string]string) string {
		return fmt.Sprintf(`Diagnose the container %q on Docker Commander.

Steps:
1. get_container to read its state, health, restart count, image and mounts.
2. container_logs to read recent output and look for errors or crash loops.
3. stats_overview (and metrics_history for trends) to check CPU/memory pressure.

Then give a concise root-cause assessment and a recommended fix. Do NOT restart or change anything without explicit confirmation from the user.`, arg(args, "container"))
	}))

	s.AddPrompt(&mcpsdk.Prompt{
		Name:        "resource_hogs",
		Description: "Find the containers consuming the most CPU and memory.",
	}, promptText(func(map[string]string) string {
		return `Identify the heaviest resource consumers on the default host.

Use stats_overview for a current snapshot, and metrics_history to confirm whether a spike is sustained or transient for the top candidates. Report the worst offenders for CPU and for memory with concrete numbers, and suggest actions (scale, limit, investigate). Recommend only — do not stop or restart anything without confirmation.`
	}))

	s.AddPrompt(&mcpsdk.Prompt{
		Name:        "safe_redeploy",
		Description: "Walk through a safe redeploy of a managed Compose project.",
		Arguments: []*mcpsdk.PromptArgument{
			{Name: "project", Description: "managed project name", Required: true},
		},
	}, promptText(func(args map[string]string) string {
		return fmt.Sprintf(`Guide a safe redeploy of the managed Compose project %q.

Steps:
1. list_managed_projects to confirm the project exists, note its id, and whether it is currently deployed.
2. Review its compose (get_compose / the compose-file resource) and summarize what will change.
3. Summarize the plan and ask the user to confirm BEFORE acting.
4. On confirmation, deploy_project (compose up -d) and report the output. Only use down_project if the user explicitly asks to bring it down.

Never run down_project without explicit confirmation — it stops and removes containers.`, arg(args, "project"))
	}))
}

// promptText adapts a plain template function into a PromptHandler that returns
// a single user message.
func promptText(build func(args map[string]string) string) mcpsdk.PromptHandler {
	return func(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
		var args map[string]string
		if req != nil && req.Params != nil {
			args = req.Params.Arguments
		}
		return &mcpsdk.GetPromptResult{
			Messages: []*mcpsdk.PromptMessage{
				{Role: "user", Content: &mcpsdk.TextContent{Text: build(args)}},
			},
		}, nil
	}
}

func arg(args map[string]string, key string) string {
	if args == nil {
		return ""
	}
	return args[key]
}
