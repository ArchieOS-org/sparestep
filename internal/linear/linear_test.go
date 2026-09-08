package linear

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeMCP struct {
	mu                                  sync.Mutex
	tools                               []Tool
	createCalls, searchCalls, teamCalls int
	create                              func(context.Context, map[string]any) (ToolResult, error)
	search                              ToolResult
}

func (f *fakeMCP) ListTools(context.Context) ([]Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Tool(nil), f.tools...), nil
}
func (f *fakeMCP) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	f.mu.Lock()
	switch normalizeToolName(name) {
	case "create_issue":
		f.createCalls++
	case "search_issues":
		f.searchCalls++
	case "list_teams":
		f.teamCalls++
	}
	fn, result := f.create, f.search
	f.mu.Unlock()
	switch normalizeToolName(name) {
	case "get_project":
		return ToolResult{StructuredContent: map[string]any{
			"id": "project", "name": "Dispatch",
			"status":     map[string]any{"id": "started", "name": "In Progress", "type": "started"},
			"teams":      []any{map[string]any{"id": "team", "name": "Dispatch", "key": "DIS"}},
			"milestones": []any{}, "canceledAt": nil,
		}}, nil
	case "get_user":
		return ToolResult{StructuredContent: map[string]any{"id": "noah-id", "name": "Noah", "active": true}}, nil
	case "list_issue_statuses":
		return ToolResult{StructuredContent: map[string]any{"statuses": []any{map[string]any{"id": "backlog", "name": "Backlog", "type": "backlog"}}}}, nil
	case "list_issue_labels":
		return ToolResult{StructuredContent: map[string]any{"labels": []any{
			map[string]any{"id": "type", "name": "Bug"},
			map[string]any{"id": "area", "name": "Capture", "parent": "area"},
		}, "hasNextPage": false}}, nil
	}
	if normalizeToolName(name) == "create_issue" && fn != nil {
		return fn(ctx, args)
	}
	if normalizeToolName(name) == "search_issues" {
		if obj, ok := result.StructuredContent.(map[string]any); ok {
			copy := map[string]any{}
			for k, v := range obj {
				copy[k] = v
			}
			copy["description"] = args["query"]
			result.StructuredContent = copy
		}
		return result, nil
	}
	return ToolResult{}, nil
}
func (f *fakeMCP) Close() error { return nil }

func testDestination() Destination {
	return Destination{TeamID: "team", ProjectID: "project", TypeLabelID: "type", AreaLabelID: "area", AssigneeID: "noah"}
}
func testItem(task string) Item {
	return NewItem(ItemInput{TaskID: task, Repository: "github.com/acme/app", Component: "capture", Problem: "same setup error", Title: "Fix setup", Criteria: []string{"command completes"}, Evidence: []string{"reproduction recorded"}})
}
func testOutbox(t *testing.T) (*Outbox, *fakeMCP) {
	t.Helper()
	o, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Configure(testDestination()); err != nil {
		t.Fatal(err)
	}
	f := &fakeMCP{tools: []Tool{{Name: "create_issue", InputSchema: map[string]any{"properties": map[string]any{"title": map[string]any{}, "description": map[string]any{}, "team_id": map[string]any{}, "project_id": map[string]any{}, "label_ids": map[string]any{}}}}, {Name: "get_project"}, {Name: "get_user"}, {Name: "list_issue_statuses"}, {Name: "list_issue_labels"}, {Name: "search_issues", InputSchema: map[string]any{"properties": map[string]any{"query": map[string]any{}}}}}}
	o.SetClient(f)
	return o, f
}

func TestEnqueueDeduplicatesStableFingerprintConcurrently(t *testing.T) {
	o, _ := testOutbox(t)
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			item := testItem("task-" + string(rune('a'+n)))
			item.Title = "wording " + item.Title
			if _, err := o.Enqueue(item); err != nil {
				t.Errorf("enqueue: %v", err)
			}
		}(n)
	}
	wg.Wait()
	items, err := o.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d records, want one stable fingerprint", len(items))
	}
	if items[0].TaskID == "" {
		t.Fatal("task id lost")
	}
}

func TestDuplicateProblemKeepsTaskReferences(t *testing.T) {
	o, _ := testOutbox(t)
	if _, err := o.Enqueue(testItem("task-one")); err != nil {
		t.Fatal(err)
	}
	if saved, err := o.Enqueue(testItem("task-two")); err != nil || saved.TaskID != "task-two" {
		t.Fatalf("duplicate enqueue: item=%+v err=%v", saved, err)
	}
	one, err := o.Items("task-one")
	if err != nil || len(one) != 1 {
		t.Fatalf("task one refs: %+v %v", one, err)
	}
	two, err := o.Items("task-two")
	if err != nil || len(two) != 1 {
		t.Fatalf("task two refs: %+v %v", two, err)
	}
	if one[0].ID != two[0].ID {
		t.Fatal("duplicate task references created separate outbox items")
	}
}

func TestFlushTimeoutAmbiguousThenReconcilesWithoutBlindRetry(t *testing.T) {
	o, f := testOutbox(t)
	_, err := o.Enqueue(testItem("task"))
	if err != nil {
		t.Fatal(err)
	}
	f.create = func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{}, context.DeadlineExceeded
	}
	res, err := o.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ambiguous != 1 {
		t.Fatalf("ambiguous=%d", res.Ambiguous)
	}
	items, _ := o.Pending()
	if items[0].Status != StatusAmbiguous || items[0].IssueURL != "" {
		t.Fatalf("unexpected timeout state: %+v", items[0])
	}
	f.mu.Lock()
	f.search = ToolResult{StructuredContent: map[string]any{"url": "https://linear.app/DIS/issue/DIS-42/reconciled"}}
	f.mu.Unlock()
	res, err = o.Flush(context.Background(), FlushOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	items, _ = o.Pending()
	if len(items) != 0 || res.Created != 0 {
		t.Fatalf("reconcile should not recreate: pending=%+v result=%+v", items, res)
	}
	f.mu.Lock()
	calls := f.createCalls
	search := f.searchCalls
	f.mu.Unlock()
	if calls != 1 || search != 1 {
		t.Fatalf("calls create=%d search=%d", calls, search)
	}
}

func TestExpiredLeaseIsReconciledBeforeAnyCreate(t *testing.T) {
	o, f := testOutbox(t)
	item, err := o.Enqueue(testItem("task"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.db.Exec(`UPDATE linear_outbox SET status=?,lease_until=? WHERE id=?`, string(StatusSending), time.Now().Add(-time.Minute).UnixNano(), item.ID); err != nil {
		t.Fatal(err)
	}
	f.search = ToolResult{StructuredContent: []any{
		map[string]any{"url": "https://linear.app/DIS/issue/DIS-1/unrelated", "description": "other finding"},
		map[string]any{"url": "https://linear.app/DIS/issue/DIS-2/matching", "description": "<!-- sparestep:fingerprint=" + item.ID + " -->"},
	}}
	res, err := o.Flush(context.Background(), FlushOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempted != 0 {
		t.Fatalf("reconciled item was recreated: %+v", res)
	}
	items, _ := o.Pending()
	if len(items) != 0 || items == nil {
		t.Fatalf("expired lease was not settled: %+v", items)
	}
	f.mu.Lock()
	calls := f.createCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("create called after crash: %d", calls)
	}
}

func TestFlushTaskDoesNotBlockOnEarlierForeignTask(t *testing.T) {
	o, f := testOutbox(t)
	first := testItem("task-one")
	first.Problem = "first"
	second := testItem("task-two")
	second.Problem = "second"
	if _, err := o.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	f.create = func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{StructuredContent: map[string]any{"url": "https://linear.app/DIS/issue/DIS-9/created"}}, nil
	}
	res, err := o.Flush(context.Background(), FlushOptions{TaskID: "task-two"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 {
		t.Fatalf("foreign task blocked flush: %+v", res)
	}
	items, _ := o.Items("task-one")
	if len(items) != 1 || items[0].Status != StatusQueued {
		t.Fatalf("foreign task changed: %+v", items)
	}
}

func TestFlushRevokedAuthIsVisibleAndDoesNotRetry(t *testing.T) {
	o, f := testOutbox(t)
	_, _ = o.Enqueue(testItem("task"))
	f.create = func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{}, errors.New("401 unauthorized")
	}
	res, err := o.Flush(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Blocked != 1 {
		t.Fatalf("blocked=%d", res.Blocked)
	}
	items, _ := o.Pending()
	if items[0].Status != StatusAuthRequired {
		t.Fatalf("state=%s", items[0].Status)
	}
	f.mu.Lock()
	calls := f.createCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("create calls=%d", calls)
	}
	_, _ = o.Flush(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createCalls != calls {
		t.Fatalf("auth_required item was retried")
	}
}

func TestFlushRequiresConfirmedURL(t *testing.T) {
	o, f := testOutbox(t)
	_, _ = o.Enqueue(testItem("task"))
	f.create = func(context.Context, map[string]any) (ToolResult, error) {
		return ToolResult{StructuredContent: map[string]any{"id": "issue-id"}}, nil
	}
	_, _ = o.Flush(context.Background())
	items, _ := o.Pending()
	if items[0].Status != StatusAmbiguous || items[0].IssueURL != "" {
		t.Fatalf("unconfirmed response: %+v", items[0])
	}
}

func TestComposeBodyBoundsAndRedacts(t *testing.T) {
	body := ComposeBody("Authorization: Bearer very-secret-token", []string{"scenario"}, []string{"/home/noah/private/customer.txt"})
	if len([]rune(body)) > 12000 {
		t.Fatal("body not bounded")
	}
	if body == "" || contains(body, "very-secret-token") || contains(body, "/home/noah/private") {
		t.Fatalf("body leaked secret: %s", body)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
