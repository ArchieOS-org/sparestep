package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

type FlushResult struct {
	Attempted int
	Created   int
	Queued    int
	Ambiguous int
	Blocked   int
	Items     []Item
}

type flushOptions struct {
	TaskID    string
	Limit     int
	Reconcile bool
}

// Flush validates the destination once, claims records with a lease, and
// sends only confirmed creates. Variadic arguments keep the API convenient for
// CLI callers that want a task ID or limit without making the common Flush(ctx)
// call verbose.
func (o *Outbox) Flush(ctx context.Context, options ...any) (FlushResult, error) {
	var opts flushOptions
	for _, raw := range options {
		switch v := raw.(type) {
		case string:
			opts.TaskID = v
		case int:
			opts.Limit = v
		case bool:
			opts.Reconcile = v
		case FlushOptions:
			opts.TaskID, opts.Limit, opts.Reconcile = v.TaskID, v.Limit, v.Reconcile
		case *FlushOptions:
			if v != nil {
				opts.TaskID, opts.Limit, opts.Reconcile = v.TaskID, v.Limit, v.Reconcile
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return FlushResult{}, err
	}
	o.mu.RLock()
	client, destination := o.client, o.destination
	o.mu.RUnlock()
	if client == nil {
		return FlushResult{}, ErrNotConnected
	}
	if err := validateDestination(destination); err != nil {
		return FlushResult{}, err
	}
	// Move expired in-flight calls to ambiguous before discovery, policy reads,
	// or claiming another item. A crash can happen after Linear creates the
	// issue and before the local URL is persisted.
	if err := o.expireSending(); err != nil {
		return FlushResult{}, err
	}
	if err := o.validateItemLabels(destination, opts.TaskID); err != nil {
		o.markQueuedAttention(err.Error(), opts.TaskID)
		return FlushResult{}, err
	}
	if err := o.discover(ctx); err != nil {
		return FlushResult{}, err
	}
	if err := o.validateLivePolicy(ctx, opts.TaskID); err != nil {
		if isAuthError(err) {
			o.markQueuedAuth(err.Error(), opts.TaskID)
		} else if !isTransientPolicyError(err) {
			o.markQueuedAttention(err.Error(), opts.TaskID)
		}
		return FlushResult{}, err
	}
	if opts.Reconcile {
		if err := o.reconcileAmbiguous(ctx, opts.TaskID); err != nil && !isAuthError(err) {
			return FlushResult{}, err
		}
	}

	var result FlushResult
	for {
		if opts.Limit > 0 && result.Attempted >= opts.Limit {
			break
		}
		item, ok, err := o.claim(opts.TaskID)
		if err != nil {
			return result, err
		}
		if !ok {
			break
		}
		result.Attempted++
		created, stateErr := o.create(ctx, item)
		if stateErr == nil {
			result.Created++
			result.Items = append(result.Items, created)
			continue
		}
		if errors.Is(stateErr, ErrAmbiguous) {
			result.Ambiguous++
			result.Items = append(result.Items, created)
		} else {
			result.Blocked++
			result.Items = append(result.Items, created)
		}
	}
	if pending, err := o.Pending(); err == nil {
		result.Queued = len(pending)
	}
	return result, nil
}

func (o *Outbox) validateItemLabels(d Destination, taskID string) error {
	if !dispatchPolicy(d) {
		return nil
	}
	items, err := o.Pending()
	if taskID != "" {
		items, err = o.Items(taskID)
	}
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status != StatusQueued {
			continue
		}
		typeLabel, areaLabel := item.TypeLabelID, item.AreaLabelID
		if typeLabel == "" {
			typeLabel = item.TypeLabel
		}
		if areaLabel == "" {
			areaLabel = item.AreaLabel
		}
		if typeLabel == "" {
			typeLabel = d.typeLabel()
		}
		if areaLabel == "" {
			areaLabel = d.areaLabel()
		}
		if typeLabel == "" || areaLabel == "" {
			_ = o.setState(item.ID, StatusNeedsAttention, "", "each issue requires exactly one type label and one area label")
			return errors.New("linear: issue type and area labels are required")
		}
	}
	return nil
}

type FlushOptions struct {
	TaskID    string
	Limit     int
	Reconcile bool
}

func (o *Outbox) discover(ctx context.Context) error {
	o.mu.RLock()
	loaded := o.toolsLoaded
	c := o.client
	o.mu.RUnlock()
	if loaded {
		return nil
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		if isAuthError(err) {
			_ = o.markAuthRequired(err.Error())
		}
		return err
	}
	if len(tools) == 0 {
		return errors.New("linear: MCP server advertised no tools")
	}
	tools = sortedTools(tools)
	var create bool
	for _, t := range tools {
		n := normalizeToolName(t.Name)
		if n == "create_issue" || n == "save_issue" || strings.HasSuffix(n, "_create_issue") || strings.HasSuffix(n, "_save_issue") || (strings.Contains(n, "issue") && strings.Contains(n, "create")) {
			create = true
			break
		}
	}
	if !create {
		return errors.New("linear: connected MCP server has no issue creation tool")
	}
	o.mu.Lock()
	o.tools = tools
	o.toolsLoaded = true
	o.mu.Unlock()
	return nil
}

func (o *Outbox) tool(name string) (Tool, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, t := range o.tools {
		if normalizeToolName(t.Name) == name {
			return t, true
		}
	}
	for _, t := range o.tools {
		n := normalizeToolName(t.Name)
		if strings.HasSuffix(n, "_"+name) {
			return t, true
		}
	}
	return Tool{}, false
}

func (o *Outbox) create(ctx context.Context, item Item) (Item, error) {
	tool, ok := o.creationTool()
	if !ok {
		_ = o.setState(item.ID, StatusNeedsAttention, "", "create issue tool not available")
		return o.itemByFingerprint(itemFingerprint(item))
	}
	args := createArguments(tool, item, o.Destination())
	result, err := o.clientCall(ctx, tool.Name, args)
	if err != nil {
		if isAuthError(err) {
			_ = o.setState(item.ID, StatusAuthRequired, "", err.Error())
			got, _ := o.itemByFingerprint(itemFingerprint(item))
			return got, ErrAuthRequired
		}
		if isAmbiguousError(err) {
			_ = o.setState(item.ID, StatusAmbiguous, "", err.Error())
			got, _ := o.itemByFingerprint(itemFingerprint(item))
			return got, ErrAmbiguous
		}
		_ = o.setState(item.ID, StatusAmbiguous, "", err.Error())
		got, _ := o.itemByFingerprint(itemFingerprint(item))
		return got, ErrAmbiguous
	}
	if result.IsError {
		msg := resultText(result)
		if isAuthError(errors.New(msg)) {
			_ = o.setState(item.ID, StatusAuthRequired, "", msg)
		} else {
			_ = o.setState(item.ID, StatusNeedsAttention, "", msg)
		}
		got, _ := o.itemByFingerprint(itemFingerprint(item))
		return got, errors.New(msg)
	}
	issueURL := confirmedURL(result)
	if issueURL == "" {
		_ = o.setState(item.ID, StatusAmbiguous, "", "create response did not contain a confirmed Linear issue URL")
		got, _ := o.itemByFingerprint(itemFingerprint(item))
		return got, ErrAmbiguous
	}
	if err := o.setState(item.ID, StatusCreated, issueURL, ""); err != nil {
		return Item{}, err
	}
	got, err := o.itemByFingerprint(itemFingerprint(item))
	return got, err
}

func itemFingerprint(item Item) string {
	key := item.ProblemKey
	if key == "" {
		key = item.Problem
	}
	return fingerprint(item.Repository, item.Component, key)
}

func (o *Outbox) clientCall(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	o.mu.RLock()
	c := o.client
	o.mu.RUnlock()
	return c.CallTool(ctx, name, args)
}

func (o *Outbox) creationTool() (Tool, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, t := range o.tools {
		n := normalizeToolName(t.Name)
		if n == "create_issue" || n == "save_issue" || strings.HasSuffix(n, "_create_issue") || strings.HasSuffix(n, "_save_issue") {
			return t, true
		}
	}
	for _, t := range o.tools {
		n := normalizeToolName(t.Name)
		if strings.Contains(n, "issue") && strings.Contains(n, "create") {
			return t, true
		}
	}
	return Tool{}, false
}

func (o *Outbox) validateLivePolicy(ctx context.Context, taskID string) error {
	return o.validateDispatchPolicy(ctx, taskID)
}

func policyReadArguments(tool Tool, d Destination) map[string]any {
	all := map[string]any{"team_id": d.team(), "teamId": d.team(), "team": d.team(), "project_id": d.project(), "projectId": d.project(), "project": d.project(), "query": d.project(), "search": d.project(), "id": d.project(), "includeMilestones": true, "include_milestones": true}
	props := schemaProperties(tool.InputSchema)
	out := map[string]any{}
	for key := range props {
		if value, ok := all[key]; ok && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		for _, key := range schemaRequired(tool.InputSchema) {
			if value, ok := all[key]; ok {
				out[key] = value
			}
		}
	}
	return out
}

func (o *Outbox) firstTool(names []string) (Tool, bool) {
	for _, name := range names {
		if t, ok := o.tool(name); ok {
			return t, true
		}
	}
	return Tool{}, false
}

func (o *Outbox) reconcileAmbiguous(ctx context.Context, taskID string) error {
	tool, ok := o.firstTool([]string{"search_issues", "list_issues", "get_issue"})
	if !ok {
		return nil
	}
	query := `SELECT id,task_id,repository,component,problem,problem_key,type_label_id,area_label_id,surface_label_id,title,body,status,issue_url,last_error,attempts,lease_until,created_at,updated_at FROM linear_outbox WHERE status=?`
	args := []any{string(StatusAmbiguous)}
	if taskID != "" {
		query += ` AND EXISTS (SELECT 1 FROM linear_outbox_tasks r WHERE r.item_id=linear_outbox.id AND r.task_id=?)`
		args = append(args, taskID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := o.db.Query(query, args...)
	if err != nil {
		return err
	}
	items, err := scanItems(rows)
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		marker := itemFingerprint(item)
		result, callErr := o.clientCall(ctx, tool.Name, issueSearchArguments(tool, marker))
		if callErr != nil {
			if isAuthError(callErr) {
				_ = o.markAuthRequired(callErr.Error())
			}
			return callErr
		}
		if result.IsError {
			continue
		}
		if u := confirmedURLForMarker(result, marker); u != "" {
			_ = o.setState(item.ID, StatusCreated, u, "")
		}
	}
	return nil
}

func issueSearchArguments(tool Tool, marker string) map[string]any {
	all := map[string]any{"query": marker, "search": marker, "term": marker, "text": marker, "fields": []string{"id", "description", "url"}}
	props := schemaProperties(tool.InputSchema)
	out := map[string]any{}
	for key := range props {
		if v, ok := all[key]; ok && v != "" {
			out[key] = v
		}
	}
	if len(out) == 0 {
		for _, key := range schemaRequired(tool.InputSchema) {
			if v, ok := all[key]; ok && v != "" {
				out[key] = v
			}
		}
	}
	return out
}

func (o *Outbox) markQueuedAttention(msg, taskID string) {
	if taskID == "" {
		_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=?`, string(StatusNeedsAttention), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusQueued))
		return
	}
	_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=? AND EXISTS (SELECT 1 FROM linear_outbox_tasks r WHERE r.item_id=linear_outbox.id AND r.task_id=?)`, string(StatusNeedsAttention), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusQueued), taskID)
}

func (o *Outbox) markQueuedAuth(msg, taskID string) {
	if taskID == "" {
		_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=?`, string(StatusAuthRequired), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusQueued))
		return
	}
	_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=? AND EXISTS (SELECT 1 FROM linear_outbox_tasks r WHERE r.item_id=linear_outbox.id AND r.task_id=?)`, string(StatusAuthRequired), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusQueued), taskID)
}

func isTransientPolicyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "timed out", "connection", "eof", "temporarily", "unavailable"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func createArguments(tool Tool, item Item, d Destination) map[string]any {
	body := boundedRedacted(item.Body+"\n\n<!-- sparestep:fingerprint="+itemFingerprint(item)+" -->", 12200)
	state := d.State
	if state == "" {
		state = "backlog"
	}
	typeLabel, areaLabel, surfaceLabel := item.TypeLabelID, item.AreaLabelID, item.SurfaceLabelID
	if typeLabel == "" {
		typeLabel = item.TypeLabel
	}
	if areaLabel == "" {
		areaLabel = item.AreaLabel
	}
	if surfaceLabel == "" {
		surfaceLabel = item.SurfaceLabel
	}
	if typeLabel == "" {
		typeLabel = d.typeLabel()
	}
	if areaLabel == "" {
		areaLabel = d.areaLabel()
	}
	if surfaceLabel == "" {
		surfaceLabel = d.surfaceLabel()
	}
	all := map[string]any{
		"title": item.Title, "description": body, "body": body,
		"team_id": d.team(), "teamId": d.team(), "team": d.team(),
		"project_id": d.project(), "projectId": d.project(), "project": d.project(),
		"assignee_id": d.assignee(), "assigneeId": d.assignee(), "assignee": d.assignee(),
		"milestone_id": d.milestone(), "milestoneId": d.milestone(), "milestone": d.milestone(),
		"state_id": state, "stateId": state, "state": state,
	}
	labels := []string{}
	for _, label := range []string{typeLabel, areaLabel, surfaceLabel} {
		if label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) > 0 {
		all["label_ids"] = labels
		all["labelIds"] = all["label_ids"]
		all["labels"] = all["label_ids"]
	}
	props := schemaProperties(tool.InputSchema)
	if len(props) == 0 {
		out := map[string]any{}
		for k, v := range all {
			if v != "" && !(k == "body" || k == "teamId" || k == "projectId" || k == "assigneeId" || k == "labelIds" || k == "milestoneId") {
				out[k] = v
			}
		}
		return out
	}
	out := map[string]any{}
	for key := range props {
		if v, ok := all[key]; ok && v != "" {
			out[key] = v
		}
	}
	// A schema that only says "input" still benefits from canonical fields.
	if len(out) == 0 {
		for _, key := range []string{"title", "description", "team_id", "project_id", "assignee_id", "label_ids", "milestone_id"} {
			if v, ok := all[key]; ok && v != "" {
				out[key] = v
			}
		}
	}
	return out
}

func schemaProperties(schema any) map[string]any {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var obj struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(b, &obj) != nil {
		return nil
	}
	return obj.Properties
}

func schemaRequired(schema any) []string {
	b, _ := json.Marshal(schema)
	var v struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(b, &v)
	return v.Required
}

func validIssueURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "linear.app" && u.User == nil && strings.Contains(u.Path, "/issue/")
}

// Only an issue object's own URL is confirmation. A link quoted in its
// description or in another search result is not the created issue.
func confirmedURL(r ToolResult) string                         { return issueResultURL(r, "") }
func confirmedURLForMarker(r ToolResult, marker string) string { return issueResultURL(r, marker) }
func issueResultURL(r ToolResult, marker string) string {
	urls := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			matched := marker == ""
			for _, key := range []string{"description", "body"} {
				if value, ok := x[key].(string); ok && strings.Contains(value, marker) {
					matched = true
				}
			}
			if address, ok := x["url"].(string); ok && matched && validIssueURL(address) {
				urls[address] = true
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(r.StructuredContent)
	for _, c := range r.Content {
		var v any
		if json.Unmarshal([]byte(c.Text), &v) == nil {
			walk(v)
		}
	}
	if len(urls) == 1 {
		for address := range urls {
			return address
		}
	}
	return ""
}

func resultText(r ToolResult) string {
	if s, ok := r.StructuredContent.(string); ok && s != "" {
		return boundedRedacted(s, 2000)
	}
	var parts []string
	for _, c := range r.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 {
		return "MCP tool returned an error"
	}
	return boundedRedacted(strings.Join(parts, " "), 2000)
}
