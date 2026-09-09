package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const (
	liveDispatchTeamID  = "11111111-1111-4111-8111-111111111111"
	liveDispatchProject = "22222222-2222-4222-8222-222222222222"
	liveGenericTeamID   = "33333333-3333-4333-8333-333333333333"
	liveGenericProject  = "44444444-4444-4444-8444-444444444444"
	liveUserID          = "55555555-5555-4555-8555-555555555555"
	liveBacklogID       = "66666666-6666-4666-8666-666666666666"
	liveBugID           = "77777777-7777-4777-8777-777777777777"
	liveCaptureID       = "88888888-8888-4888-8888-888888888888"
)

type liveShapeMCP struct {
	mu sync.Mutex

	generic         bool
	canceledProject bool
	noURLOnCreate   bool
	createCalls     int
	listCalls       int
	lastListQuery   string
	tools           []Tool
}

func newLiveShapeMCP(generic, canceled, noURL bool) *liveShapeMCP {
	stringProperty := func() map[string]any { return map[string]any{"type": "string"} }
	return &liveShapeMCP{
		generic:         generic,
		canceledProject: canceled,
		noURLOnCreate:   noURL,
		tools: []Tool{
			{Name: "get_project", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"query": stringProperty(), "includeMilestones": map[string]any{"type": "boolean"}},
				"required":   []string{"query"},
			}},
			{Name: "get_user", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"query": stringProperty()}, "required": []string{"query"},
			}},
			{Name: "list_issue_statuses", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"team": stringProperty()}, "required": []string{"team"},
			}},
			{Name: "list_issue_labels", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"team": stringProperty()}, "required": []string{"team"},
			}},
			{Name: "list_issues", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"query":  stringProperty(),
					"fields": map[string]any{"type": "array", "items": stringProperty()},
				},
			}},
			{Name: "save_issue", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"title":       stringProperty(),
					"description": stringProperty(),
					"team_id":     stringProperty(),
					"project_id":  stringProperty(),
					"assignee_id": stringProperty(),
					"label_ids":   map[string]any{"type": "array", "items": stringProperty()},
					"state_id":    stringProperty(),
				},
				"required": []string{"title", "team_id"},
			}},
		},
	}
}

func (f *liveShapeMCP) ListTools(context.Context) ([]Tool, error) {
	return append([]Tool(nil), f.tools...), nil
}

func (f *liveShapeMCP) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	tool, ok := f.tool(name)
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool %q", name)
	}
	if err := validateLiveSchema(tool, args); err != nil {
		return ToolResult{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch normalizeToolName(name) {
	case "get_project":
		return f.projectResult(), nil
	case "get_user":
		// This deliberately has no isMe convenience field: the policy must
		// accept a single user returned for query="me" by its UUID/name.
		return liveTextResult(map[string]any{"id": liveUserID, "name": "Noah", "active": true, "teams": []any{map[string]any{"id": liveDispatchTeamID, "name": "Dispatch"}, map[string]any{"id": liveGenericTeamID, "name": "Other"}}}), nil
	case "list_issue_statuses":
		return liveTextResult(map[string]any{"statuses": []any{map[string]any{
			"id": liveBacklogID, "name": "Backlog", "type": "backlog",
		}}}), nil
	case "list_issue_labels":
		if f.generic {
			return liveTextResult(map[string]any{"labels": []any{}, "hasNextPage": false}), nil
		}
		return liveTextResult(map[string]any{"labels": []any{
			map[string]any{"id": liveBugID, "name": "Bug"},
			map[string]any{"id": "99999999-9999-4999-8999-999999999999", "name": "Job"},
			map[string]any{"id": liveCaptureID, "name": "capture", "parent": "area"},
		}, "hasNextPage": false}), nil
	case "list_issues":
		query, _ := args["query"].(string)
		f.listCalls++
		f.lastListQuery = query
		return liveTextResult(map[string]any{"issues": []any{
			map[string]any{"id": "unrelated", "identifier": "DIS-1", "url": "https://linear.app/DIS/issue/DIS-1-unrelated", "description": "different marker"},
			map[string]any{"id": "matching", "identifier": "DIS-2", "url": "https://linear.app/DIS/issue/DIS-2-matching", "description": query},
		}, "hasNextPage": false}), nil
	case "save_issue":
		f.createCalls++
		if f.noURLOnCreate {
			return liveTextResult(map[string]any{"issue": map[string]any{"id": "created-without-url"}}), nil
		}
		return liveTextResult(map[string]any{"issue": map[string]any{
			"id": "created", "identifier": "DIS-3", "url": "https://linear.app/DIS/issue/DIS-3-created",
		}}), nil
	default:
		return ToolResult{}, fmt.Errorf("unhandled tool %q", name)
	}
}

func (f *liveShapeMCP) tool(name string) (Tool, bool) {
	for _, tool := range f.tools {
		if normalizeToolName(tool.Name) == normalizeToolName(name) {
			return tool, true
		}
	}
	return Tool{}, false
}

func (f *liveShapeMCP) projectResult() ToolResult {
	teamID, teamName := liveDispatchTeamID, "Dispatch"
	projectID, projectName := liveDispatchProject, "Dispatch v1"
	if f.generic {
		teamID, teamName = liveGenericTeamID, "Generic"
		projectID, projectName = liveGenericProject, "Generic project"
	}
	status := map[string]any{"id": "started", "name": "In Progress", "type": "started"}
	canceledAt := any(nil)
	if f.canceledProject {
		status = map[string]any{"id": "canceled", "name": "Canceled", "type": "canceled"}
		canceledAt = "2026-09-08T00:00:00Z"
	}
	return liveTextResult(map[string]any{
		"id": projectID, "name": projectName, "status": status,
		"teams":      []any{map[string]any{"id": teamID, "name": teamName, "key": strings.ToUpper(teamName[:3])}},
		"milestones": []any{}, "canceledAt": canceledAt,
	})
}

func (f *liveShapeMCP) Close() error { return nil }

func liveTextResult(value any) ToolResult {
	b, _ := json.Marshal(value)
	return ToolResult{Content: []Content{{Type: "text", Text: string(b)}}}
}

func validateLiveSchema(tool Tool, args map[string]any) error {
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		return errors.New("live fixture schema is not an object")
	}
	properties, _ := schema["properties"].(map[string]any)
	for key, value := range args {
		if value == nil {
			return fmt.Errorf("%s received null %q", tool.Name, key)
		}
		if _, ok := properties[key]; !ok {
			return fmt.Errorf("%s received unknown argument %q", tool.Name, key)
		}
	}
	required, _ := schema["required"].([]string)
	for _, key := range required {
		value, ok := args[key]
		if !ok || value == nil {
			return fmt.Errorf("%s missing required argument %q", tool.Name, key)
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s received empty required argument %q", tool.Name, key)
		}
	}
	return nil
}

func liveDispatchItem(task string, typeLabel, areaLabel string) Item {
	return NewItem(ItemInput{
		TaskID: task, Repository: "github.com/acme/dispatch", Component: "publisher",
		Problem: "live metadata fixture finding", Title: "Fix live metadata fixture",
		TypeLabel: typeLabel, AreaLabel: areaLabel,
		Criteria: []string{"the issue is filed with live policy values"},
		Evidence: []string{"the fixture reproduces the finding"},
	})
}

func liveDestination(generic bool) Destination {
	if generic {
		return Destination{Team: "Generic", Project: "Generic project", Assignee: "me"}
	}
	return Destination{Team: "Dispatch", Project: "Dispatch v1", Assignee: "me"}
}

func openLiveFixture(t *testing.T, fake *liveShapeMCP, destination Destination) *Outbox {
	t.Helper()
	o, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Configure(destination); err != nil {
		t.Fatal(err)
	}
	o.SetClient(fake)
	t.Cleanup(func() { _ = o.Close() })
	return o
}

func TestLiveShapesSuccessfulDispatchWithDefaultNames(t *testing.T) {
	fake := newLiveShapeMCP(false, false, false)
	o := openLiveFixture(t, fake, liveDestination(false))
	if _, err := o.Enqueue(liveDispatchItem("live-success", "Bug", "capture")); err != nil {
		t.Fatal(err)
	}
	result, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-success"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Attempted != 1 {
		t.Fatalf("unexpected flush result: %+v", result)
	}
	if items, err := o.Pending(); err != nil || len(items) != 0 {
		t.Fatalf("created issue remained pending: items=%+v err=%v", items, err)
	}
}

func TestLiveShapesCanceledProjectRefused(t *testing.T) {
	fake := newLiveShapeMCP(false, true, false)
	o := openLiveFixture(t, fake, liveDestination(false))
	if _, err := o.Enqueue(liveDispatchItem("live-canceled", "Bug", "capture")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-canceled"}); err == nil {
		t.Fatal("canceled project was accepted")
	}
	fake.mu.Lock()
	created := fake.createCalls
	fake.mu.Unlock()
	if created != 0 {
		t.Fatalf("canceled project reached save_issue: %d calls", created)
	}
}

func TestLiveShapesArbitraryUngroupedLabelCannotCountAsType(t *testing.T) {
	fake := newLiveShapeMCP(false, false, false)
	o := openLiveFixture(t, fake, liveDestination(false))
	if _, err := o.Enqueue(liveDispatchItem("live-arbitrary-type", "Job", "capture")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-arbitrary-type"}); err == nil {
		t.Fatal("arbitrary ungrouped label was accepted as a Dispatch type")
	}
	fake.mu.Lock()
	created := fake.createCalls
	fake.mu.Unlock()
	if created != 0 {
		t.Fatalf("invalid type reached save_issue: %d calls", created)
	}
}

func TestLiveShapesGenericTeamCanFileWithoutDispatchLabels(t *testing.T) {
	fake := newLiveShapeMCP(true, false, false)
	o := openLiveFixture(t, fake, liveDestination(true))
	item := NewItem(ItemInput{TaskID: "live-generic", Repository: "example.com/other/repo", Component: "worker", Problem: "generic fixture finding", Title: "Fix generic fixture"})
	if _, err := o.Enqueue(item); err != nil {
		t.Fatal(err)
	}
	result, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-generic"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Fatalf("generic destination was not filed: %+v", result)
	}
}

func TestLiveShapesReconcileUsesMarkerMatchingIssueURL(t *testing.T) {
	fake := newLiveShapeMCP(false, false, true)
	o := openLiveFixture(t, fake, liveDestination(false))
	if _, err := o.Enqueue(liveDispatchItem("live-reconcile", "Bug", "capture")); err != nil {
		t.Fatal(err)
	}
	first, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-reconcile"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Ambiguous != 1 {
		t.Fatalf("missing-url create was not ambiguous: %+v", first)
	}
	second, err := o.Flush(context.Background(), FlushOptions{TaskID: "live-reconcile", Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 {
		t.Fatalf("reconciliation created a duplicate: %+v", second)
	}
	items, err := o.Items("live-reconcile")
	if err != nil || len(items) != 1 || items[0].Status != StatusCreated || !strings.Contains(items[0].IssueURL, "DIS-2-matching") {
		t.Fatalf("marker did not settle matching issue: items=%+v err=%v", items, err)
	}
	fake.mu.Lock()
	creates, lists, query := fake.createCalls, fake.listCalls, fake.lastListQuery
	fake.mu.Unlock()
	if creates != 1 || lists != 1 || query != items[0].ID {
		t.Fatalf("unexpected reconciliation calls: creates=%d lists=%d query=%q", creates, lists, query)
	}
}

func TestUserDetailsPreserveAmbiguity(t *testing.T) {
	user := map[string]any{"id": "user-one", "name": "User", "teams": []any{map[string]any{"id": "team-one", "name": "Team"}}}
	data, _ := json.Marshal(user)
	result := ToolResult{StructuredContent: user, Content: []Content{{Type: "text", Text: string(data)}}}
	entities := policyUserEntities(result)
	if len(entities) != 1 {
		t.Fatalf("counted relationships or duplicate encodings as users: %d", len(entities))
	}
	if _, ok := findEntity(entities, "me"); !ok {
		t.Fatal("rejected current user")
	}
	result = ToolResult{StructuredContent: map[string]any{"users": []any{user, map[string]any{"id": "user-two", "name": "Another"}}}}
	if _, ok := findEntity(policyUserEntities(result), "me"); ok {
		t.Fatal("accepted ambiguous users")
	}
}
