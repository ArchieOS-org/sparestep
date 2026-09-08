package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func dispatchPolicy(d Destination) bool {
	return strings.EqualFold(d.team(), "DIS") || strings.EqualFold(d.team(), "Dispatch") || d.team() == "ca612367-ef47-43c8-bda5-14ac9ac9358e"
}

// validateDispatchPolicy reads the MCP read surface required by Dispatch and
// fails closed when a destination cannot be proven valid. The MCP connector
// has changed whether a result is structuredContent or JSON text over time,
// so both forms are decoded here.
func (o *Outbox) validateDispatchPolicy(ctx context.Context, taskID string) error {
	d := o.Destination()
	projectTool, ok := o.firstTool([]string{"get_project"})
	if !ok {
		return errors.New("linear: MCP policy read get_project is unavailable")
	}
	projectResult, err := o.clientCall(ctx, projectTool.Name, policyReadArguments(projectTool, d))
	if err != nil {
		return err
	}
	if projectResult.IsError {
		return fmt.Errorf("linear: get_project failed: %s", resultText(projectResult))
	}
	project := policyEntities(projectResult)
	projectEntity, ok := findEntity(project, d.project())
	if !ok {
		return errors.New("linear: selected project was not returned by live policy")
	}
	if entityCanceled([]policyEntity{projectEntity}) {
		return errors.New("linear: selected project is canceled or inactive")
	}
	projectTeam := entityRelation(projectEntity, "team")
	teams, hasTeams := projectEntity["teams"]
	if projectTeam == "" && !hasTeams {
		return errors.New("linear: selected project team relationship was not returned by live policy")
	}
	if projectTeam != "" && !hasTeams && !identityMatches(projectTeam, d.team()) {
		return errors.New("linear: selected project does not belong to the selected team")
	}
	if hasTeams && !relationContains(teams, d.team()) {
		return errors.New("linear: selected project does not belong to the selected team")
	}

	userTool, ok := o.firstTool([]string{"get_user"})
	if !ok {
		return errors.New("linear: MCP policy read get_user is unavailable")
	}
	userResult, err := o.clientCall(ctx, userTool.Name, policyUserArguments(userTool, d.assignee()))
	if err != nil {
		return err
	}
	if userResult.IsError {
		return fmt.Errorf("linear: get_user failed: %s", resultText(userResult))
	}
	assignee, ok := findEntity(policyEntities(userResult), d.assignee())
	if !ok {
		return errors.New("linear: selected assignee was not returned by live policy")
	}
	if entityCanceled([]policyEntity{assignee}) {
		return errors.New("linear: selected assignee is inactive")
	}

	statusTool, ok := o.firstTool([]string{"list_issue_statuses"})
	if !ok {
		return errors.New("linear: MCP policy read list_issue_statuses is unavailable")
	}
	statusResult, err := o.clientCall(ctx, statusTool.Name, policyTeamArguments(statusTool, d.team()))
	if err != nil {
		return err
	}
	if statusResult.IsError {
		return fmt.Errorf("linear: list_issue_statuses failed: %s", resultText(statusResult))
	}
	state := d.State
	if state == "" {
		state = "backlog"
	}
	if !entityNameMatches(policyEntities(statusResult), state) {
		return fmt.Errorf("linear: required workflow state %q was not returned by live policy", state)
	}

	labelTool, ok := o.firstTool([]string{"list_issue_labels"})
	if !ok {
		return errors.New("linear: MCP policy read list_issue_labels is unavailable")
	}
	labelResult, err := o.clientCall(ctx, labelTool.Name, policyTeamArguments(labelTool, d.team()))
	if err != nil {
		return err
	}
	if labelResult.IsError {
		return fmt.Errorf("linear: list_issue_labels failed: %s", resultText(labelResult))
	}
	labels := policyEntities(labelResult)
	items, _ := o.Pending()
	if taskID != "" {
		items, _ = o.Items(taskID)
	}
	for _, item := range items {
		if item.Status != StatusQueued {
			continue
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
		if !dispatchPolicy(d) {
			for _, label := range []string{typeLabel, areaLabel, surfaceLabel} {
				if label != "" && !entityMatches(labels, label) {
					return errors.New("linear: selected label was not returned for this team")
				}
			}
			continue
		}
		if typeLabel == "" || areaLabel == "" {
			return errors.New("linear: each issue requires exactly one type label and one area label")
		}
		if !labelMatches(labels, typeLabel, "type") || !labelMatches(labels, areaLabel, "area") {
			return errors.New("linear: selected type or area label is invalid for this team")
		}
		if surfaceLabel != "" && !labelMatches(labels, surfaceLabel, "surface") {
			return errors.New("linear: selected surface label is invalid for this team")
		}
	}

	projectHasMilestones, milestoneMetadata := entityMilestoneInfo(projectEntity)
	if !milestoneMetadata {
		return errors.New("linear: project milestone requirements were not returned by live policy")
	}
	if projectHasMilestones {
		milestoneTool, ok := o.firstTool([]string{"list_milestones"})
		if !ok {
			return errors.New("linear: project has milestones but list_milestones is unavailable")
		}
		milestoneResult, err := o.clientCall(ctx, milestoneTool.Name, policyProjectArguments(milestoneTool, d.project()))
		if err != nil {
			return err
		}
		if milestoneResult.IsError {
			return fmt.Errorf("linear: list_milestones failed: %s", resultText(milestoneResult))
		}
		milestone := d.milestone()
		if milestone == "" {
			return errors.New("linear: destination milestone is required for this project")
		}
		if !entityMatches(policyEntities(milestoneResult), milestone) {
			return errors.New("linear: selected milestone was not returned by live policy")
		}
	}
	return nil
}

type policyEntity map[string]any

func policyEntities(r ToolResult) []policyEntity {
	var values []any
	if r.StructuredContent != nil {
		values = append(values, r.StructuredContent)
	}
	for _, c := range r.Content {
		var v any
		if json.Unmarshal([]byte(c.Text), &v) == nil {
			values = append(values, v)
		}
	}
	var out []policyEntity
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if hasEntityIdentity(x) {
				out = append(out, policyEntity(x))
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
	for _, value := range values {
		walk(value)
	}
	return out
}

func hasEntityIdentity(m map[string]any) bool {
	_, id := entityValue(m, "id", "identifier", "key")
	_, name := entityValue(m, "name", "displayName", "title")
	return id || name
}
func entityValue(m map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		for actual, value := range m {
			if strings.EqualFold(actual, key) {
				if s, ok := value.(string); ok && s != "" {
					return s, true
				}
			}
		}
	}
	return "", false
}
func entityMatches(es []policyEntity, selected string) bool {
	if selected == "" {
		return false
	}
	for _, e := range es {
		if entityIdentityMatches(e, selected) {
			return true
		}
	}
	return false
}

func findEntity(es []policyEntity, selected string) (policyEntity, bool) {
	if strings.EqualFold(strings.TrimSpace(selected), "me") {
		for _, e := range es {
			if v, ok := e["isMe"].(bool); ok && v {
				return e, true
			}
		}
		// get_user(query:"me") returns the current user as the sole entity.
		// Accepting that exact shape lets callers use the MCP query alias while
		// still rejecting an ambiguous multi-user response.
		if len(es) == 1 {
			return es[0], true
		}
	}
	for _, e := range es {
		if entityIdentityMatches(e, selected) {
			return e, true
		}
	}
	return nil, false
}
func entityIdentityMatches(e policyEntity, selected string) bool {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return false
	}
	for _, key := range []string{"id", "identifier", "key", "name", "displayName", "title"} {
		if v, ok := entityValue(e, key); ok && strings.EqualFold(v, selected) {
			return true
		}
	}
	return false
}
func identityMatches(value, selected string) bool {
	return strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(selected))
}
func entityNameMatches(es []policyEntity, selected string) bool {
	selected = strings.ToLower(selected)
	for _, e := range es {
		for _, key := range []string{"id", "identifier", "key", "name", "displayName", "title"} {
			if v, ok := entityValue(e, key); ok && strings.EqualFold(v, selected) {
				return true
			}
		}
	}
	return false
}
func entityRelationID(e []policyEntity, key string) string {
	for _, v := range e {
		if id := entityRelation(v, key); id != "" {
			return id
		}
	}
	return ""
}
func entityRelation(e policyEntity, key string) string {
	for actual, value := range e {
		if strings.EqualFold(actual, key) || strings.EqualFold(actual, key+"Id") || strings.EqualFold(actual, key+"_id") {
			if s, ok := value.(string); ok {
				return s
			}
			if child, ok := value.(map[string]any); ok {
				if id, ok := entityValue(child, "id", "identifier"); ok {
					return id
				}
			}
		}
	}
	return ""
}

func relationContains(value any, selected string) bool {
	switch v := value.(type) {
	case map[string]any:
		if entityIdentityMatches(policyEntity(v), selected) {
			return true
		}
		for _, child := range v {
			if relationContains(child, selected) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if relationContains(child, selected) {
				return true
			}
		}
	}
	return false
}
func entityCanceled(es []policyEntity) bool {
	for _, e := range es {
		if canceledValue(map[string]any(e)) {
			return true
		}
	}
	return false
}

func canceledValue(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, child := range object {
		switch strings.ToLower(key) {
		case "canceledat", "cancelledat", "archivedat", "completedat":
			if text, ok := child.(string); ok && text != "" {
				return true
			}
		case "active", "isactive":
			if active, ok := child.(bool); ok && !active {
				return true
			}
		case "archived", "isarchived", "inactive":
			if yes, ok := child.(bool); ok && yes {
				return true
			}
		case "status", "state":
			values := []string{}
			if text, ok := child.(string); ok {
				values = append(values, text)
			}
			if state, ok := child.(map[string]any); ok {
				for _, field := range []string{"name", "type"} {
					if text, ok := state[field].(string); ok {
						values = append(values, text)
					}
				}
			}
			for _, text := range values {
				switch strings.ToLower(text) {
				case "canceled", "cancelled", "archived", "inactive", "completed":
					return true
				}
			}
		}
	}
	return false
}

func entityMilestoneInfo(es ...policyEntity) (hasMilestones, present bool) {
	for _, e := range es {
		for key, value := range e {
			lk := strings.ToLower(key)
			if strings.Contains(lk, "milestone") {
				present = true
				switch x := value.(type) {
				case bool:
					if x {
						hasMilestones = true
					}
				case []any:
					if len(x) > 0 {
						hasMilestones = true
					}
				case map[string]any:
					if len(x) > 0 {
						hasMilestones = true
					}
				case string:
					if strings.TrimSpace(x) != "" {
						hasMilestones = true
					}
				}
			}
		}
	}
	return hasMilestones, present
}
func labelMatches(es []policyEntity, selected, group string) bool {
	for _, e := range es {
		if !entityMatches([]policyEntity{e}, selected) {
			continue
		}
		g := entityGroup(e)
		if group == "type" && g == "" {
			name, _ := entityValue(e, "name")
			switch strings.ToLower(name) {
			case "bug", "feature", "chore", "spike", "refactor", "ui/ux", "data/backend":
				return true
			}
			return false
		}
		if g == "" || !strings.Contains(strings.ToLower(g), group) {
			continue
		}
		return true
	}
	return false
}
func entityGroup(e policyEntity) string {
	for key, value := range e {
		lk := strings.ToLower(key)
		if strings.Contains(lk, "group") || lk == "type" || lk == "category" || lk == "parent" {
			if s, ok := value.(string); ok {
				return s
			}
			if child, ok := value.(map[string]any); ok {
				if s, ok := entityValue(child, "name", "id", "identifier"); ok {
					return s
				}
			}
		}
	}
	return ""
}
func policyUserArguments(t Tool, id string) map[string]any {
	return policyArgumentSet(t, map[string]string{"id": id, "user_id": id, "userId": id, "identifier": id, "user": id, "query": id})
}
func policyTeamArguments(t Tool, id string) map[string]any {
	return policyArgumentSet(t, map[string]string{"team_id": id, "teamId": id, "team": id})
}
func policyProjectArguments(t Tool, id string) map[string]any {
	return policyArgumentSet(t, map[string]string{"project_id": id, "projectId": id, "project": id})
}
func policyArgumentSet(t Tool, all map[string]string) map[string]any {
	props := schemaProperties(t.InputSchema)
	out := map[string]any{}
	for k := range props {
		if v := all[k]; v != "" {
			out[k] = v
		}
	}
	if len(props) == 0 {
		out = map[string]any{}
		for _, key := range schemaRequired(t.InputSchema) {
			if v := all[key]; v != "" {
				out[key] = v
			}
		}
		return out
	}
	return out
}
