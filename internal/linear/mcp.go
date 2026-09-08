package linear

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sdkClient struct{ session *mcp.ClientSession }

func (c *sdkClient) ListTools(ctx context.Context) ([]Tool, error) {
	r, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(r.Tools))
	for _, t := range r.Tools {
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out, nil
}

func (c *sdkClient) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	r, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ToolResult{}, err
	}
	out := ToolResult{IsError: r.IsError, StructuredContent: r.StructuredContent}
	for _, item := range r.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			out.Content = append(out.Content, Content{Type: "text", Text: text.Text})
		} else {
			out.Content = append(out.Content, Content{Type: fmt.Sprintf("%T", item)})
		}
	}
	return out, nil
}

func (c *sdkClient) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}
