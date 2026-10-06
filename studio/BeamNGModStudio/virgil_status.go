package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	diffmatchpatch "github.com/sergi/go-diff/diffmatchpatch"
)

// VirgilSessionSummary is calculated from persisted run events and workspace
// mutations. It is intentionally not stored in the durable session row: the
// values are a read model for the editor status bar.
type VirgilSessionSummary struct {
	SessionID    string `json:"sessionId"`
	WorkspaceID  string `json:"workspaceId"`
	Status       string `json:"status"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
	ChangeMode   string `json:"changeMode,omitempty"`
	AddedLines   *int64 `json:"addedLines,omitempty"`
	RemovedLines *int64 `json:"removedLines,omitempty"`
	InputTokens  *int64 `json:"inputTokens,omitempty"`
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	TotalTokens  *int64 `json:"totalTokens,omitempty"`
	ContextUsed  *int64 `json:"contextUsed,omitempty"`
	ContextLimit *int64 `json:"contextLimit,omitempty"`
}

// agentEventMetrics is kept in agent_events.data_json under the
// "sessionMetrics" key. The runtime event is normalized at the agent
// boundary, so consumers never need to infer metrics from rendered text.
type agentEventMetrics struct {
	Path         string `json:"path,omitempty"`
	Net          bool   `json:"net,omitempty"`
	AddedLines   *int64 `json:"addedLines,omitempty"`
	RemovedLines *int64 `json:"removedLines,omitempty"`
	InputTokens  *int64 `json:"inputTokens,omitempty"`
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	TotalTokens  *int64 `json:"totalTokens,omitempty"`
	ContextUsed  *int64 `json:"contextUsed,omitempty"`
	ContextLimit *int64 `json:"contextLimit,omitempty"`
}

func (s *Store) virgilSessionSummary(ctx context.Context, record VirgilSessionRecord) (*VirgilSessionSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summary := &VirgilSessionSummary{
		SessionID:   record.ID,
		WorkspaceID: record.WorkspaceID,
		Status:      record.Status,
		StartedAt:   record.CreatedAt,
	}
	const maxSummaryRuns = 32
	runs := record.Runs
	truncated := len(runs) > maxSummaryRuns
	if truncated {
		runs = runs[len(runs)-maxSummaryRuns:]
	}
	activeRun := false
	netChanges := map[string]*agentEventMetrics{}
	var grossAdded, grossRemoved *int64
	hasGrossChanges := false
	for _, run := range runs {
		if startedBefore(run.StartedAt, summary.StartedAt) {
			summary.StartedAt = run.StartedAt
		}
		if run.Status == "running" || run.Status == "starting" {
			activeRun = true
		}
		if !activeRun && laterTimestamp(run.FinishedAt, summary.FinishedAt) {
			summary.FinishedAt = run.FinishedAt
		}
		events, err := s.ListAgentEvents(ctx, run.ID, 2000)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			metrics := decodeAgentEventMetrics(event.Data)
			if metrics == nil {
				continue
			}
			addMetric(&summary.InputTokens, metrics.InputTokens)
			addMetric(&summary.OutputTokens, metrics.OutputTokens)
			addMetric(&summary.TotalTokens, metrics.TotalTokens)
			if metrics.ContextUsed != nil {
				summary.ContextUsed = cloneInt64(metrics.ContextUsed)
			}
			if metrics.ContextLimit != nil {
				summary.ContextLimit = cloneInt64(metrics.ContextLimit)
			}
			if metrics.AddedLines == nil && metrics.RemovedLines == nil {
				continue
			}
			if metrics.Net && strings.TrimSpace(metrics.Path) != "" {
				netChanges[strings.ToLower(metrics.Path)] = metrics
				continue
			}
			hasGrossChanges = true
			addMetric(&grossAdded, metrics.AddedLines)
			addMetric(&grossRemoved, metrics.RemovedLines)
		}
	}
	if activeRun || summary.Status == "running" || summary.Status == "starting" {
		summary.FinishedAt = ""
	}
	if truncated {
		summary.AddedLines = nil
		summary.RemovedLines = nil
		summary.InputTokens = nil
		summary.OutputTokens = nil
		summary.TotalTokens = nil
		summary.ContextUsed = nil
		summary.ContextLimit = nil
		return summary, nil
	}
	if hasGrossChanges {
		summary.ChangeMode = "edits"
		summary.AddedLines = grossAdded
		summary.RemovedLines = grossRemoved
	} else if len(netChanges) > 0 {
		summary.ChangeMode = "net"
		for _, metrics := range netChanges {
			addMetric(&summary.AddedLines, metrics.AddedLines)
			addMetric(&summary.RemovedLines, metrics.RemovedLines)
		}
	}
	return summary, nil
}

func startedBefore(candidate, current string) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentAt, currentErr := time.Parse(time.RFC3339Nano, current)
	if candidateErr != nil || currentErr != nil {
		return candidate < current
	}
	return candidateAt.Before(currentAt)
}

func laterTimestamp(candidate, current string) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	candidateAt, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	currentAt, currentErr := time.Parse(time.RFC3339Nano, current)
	if candidateErr != nil || currentErr != nil {
		return candidate > current
	}
	return candidateAt.After(currentAt)
}

func addMetric(target **int64, value *int64) {
	if value == nil {
		return
	}
	if *target == nil {
		zero := int64(0)
		*target = &zero
	}
	**target += *value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func decodeAgentEventMetrics(data map[string]any) *agentEventMetrics {
	if data == nil {
		return nil
	}
	raw, ok := data["sessionMetrics"]
	if !ok {
		return nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		if json.Unmarshal(encoded, &value) != nil {
			return nil
		}
	}
	path, _ := value["path"].(string)
	net, _ := value["net"].(bool)
	metrics := &agentEventMetrics{
		Path:         path,
		Net:          net,
		AddedLines:   metricPointer(value, "addedLines"),
		RemovedLines: metricPointer(value, "removedLines"),
		InputTokens:  metricPointer(value, "inputTokens"),
		OutputTokens: metricPointer(value, "outputTokens"),
		TotalTokens:  metricPointer(value, "totalTokens"),
		ContextUsed:  metricPointer(value, "contextUsed"),
		ContextLimit: metricPointer(value, "contextLimit"),
	}
	if metrics.TotalTokens == nil && metrics.InputTokens != nil && metrics.OutputTokens != nil {
		total := *metrics.InputTokens + *metrics.OutputTokens
		metrics.TotalTokens = &total
	}
	if metrics.AddedLines == nil && metrics.RemovedLines == nil && metrics.InputTokens == nil && metrics.OutputTokens == nil && metrics.TotalTokens == nil && metrics.ContextUsed == nil && metrics.ContextLimit == nil {
		return nil
	}
	return metrics
}

func metricPointer(value map[string]any, key string) *int64 {
	child, ok := value[key]
	if !ok {
		return nil
	}
	number, ok := metricNumber(child)
	if !ok {
		return nil
	}
	return &number
}

func metricNumber(value any) (int64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		return int64(typed), typed >= 0
	case int8:
		return int64(typed), typed >= 0
	case int16:
		return int64(typed), typed >= 0
	case int32:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case uint:
		return int64(typed), uint64(typed) <= math.MaxInt64
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), typed <= math.MaxInt64
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, parsed >= 0
		}
		parsedFloat, floatErr := typed.Float64()
		if floatErr != nil {
			return 0, false
		}
		number = parsedFloat
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > math.MaxInt64 {
		return 0, false
	}
	return int64(number), true
}

func sessionMetricsFromFrame(frame map[string]any) *agentEventMetrics {
	if frame == nil {
		return nil
	}
	roots := []any{}
	for _, key := range []string{"usage", "tokenUsage", "token_usage"} {
		if value, exists := frame[key]; exists {
			roots = append(roots, value)
		}
	}
	if data, ok := frame["data"].(map[string]any); ok {
		for _, key := range []string{"usage", "tokenUsage", "token_usage"} {
			if value, exists := data[key]; exists {
				roots = append(roots, value)
			}
		}
	}
	if len(roots) == 0 {
		return nil
	}
	metrics := &agentEventMetrics{
		InputTokens:  firstMetric(roots, "inputTokens", "inputTokenCount", "promptTokens"),
		OutputTokens: firstMetric(roots, "outputTokens", "outputTokenCount", "completionTokens"),
		TotalTokens:  firstMetric(roots, "totalTokens", "totalTokenCount"),
		ContextUsed:  firstMetric(roots, "contextUsed", "contextTokens", "usedTokens"),
		ContextLimit: firstMetric(roots, "contextWindow", "modelContextWindow", "contextLimit", "maxContextTokens"),
	}
	if metrics.TotalTokens == nil && metrics.InputTokens != nil && metrics.OutputTokens != nil {
		total := *metrics.InputTokens + *metrics.OutputTokens
		metrics.TotalTokens = &total
	}
	if metrics.InputTokens == nil && metrics.OutputTokens == nil && metrics.TotalTokens == nil && metrics.ContextUsed == nil && metrics.ContextLimit == nil {
		return nil
	}
	return metrics
}

func firstMetric(roots []any, keys ...string) *int64 {
	for _, root := range roots {
		if value, ok := findMetric(root, keys...); ok {
			return &value
		}
	}
	return nil
}

func findMetric(value any, keys ...string) (int64, bool) {
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[normalizeMetricKey(key)] = struct{}{}
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, wanted := keySet[normalizeMetricKey(key)]; !wanted {
				continue
			}
			if number, ok := metricNumber(child); ok {
				return number, true
			}
		}
		for _, child := range typed {
			if number, ok := findMetric(child, keys...); ok {
				return number, true
			}
		}
	case []any:
		for _, child := range typed {
			if number, ok := findMetric(child, keys...); ok {
				return number, true
			}
		}
	}
	return 0, false
}

func normalizeMetricKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

func isWorkspaceMutation(toolName string) bool {
	return toolName == "workspace_write" || toolName == "workspace_replace"
}

func workspaceTextForMetrics(ctx context.Context, filesRoot, relativePath string) (string, bool) {
	content, err := modkit.ReadWorkspaceTextContext(ctx, filesRoot, relativePath)
	if err == nil {
		return content, true
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", true
	}
	return "", false
}

func workspaceLineMetrics(before, after string) *agentEventMetrics {
	added, removed := lineDiffCounts(before, after)
	return &agentEventMetrics{AddedLines: &added, RemovedLines: &removed}
}
func (run *agentRun) workspaceChangeMetrics(path, before, after string) *agentEventMetrics {
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	metrics := workspaceLineMetrics(before, after)
	metrics.Path = path
	if !run.sessionNetChanges || path == "" {
		return metrics
	}
	key := strings.ToLower(path)
	run.manager.sessionMetricMu.Lock()
	baselines := run.manager.sessionMutationBaselines[run.sessionID]
	if baselines == nil {
		baselines = map[string]string{}
		run.manager.sessionMutationBaselines[run.sessionID] = baselines
	}
	baseline, exists := baselines[key]
	if !exists {
		baseline = before
		baselines[key] = baseline
	}
	run.manager.sessionMetricMu.Unlock()
	metrics = workspaceLineMetrics(baseline, after)
	metrics.Path = path
	metrics.Net = true
	return metrics
}

func lineDiffCounts(before, after string) (int64, int64) {
	if before == after {
		return 0, 0
	}
	differ := diffmatchpatch.New()
	charsBefore, charsAfter, lineArray := differ.DiffLinesToChars(before, after)
	diffs := differ.DiffMain(charsBefore, charsAfter, false)
	diffs = differ.DiffCharsToLines(diffs, lineArray)
	var added, removed int64
	for _, diff := range diffs {
		count := countTextLines(diff.Text)
		switch diff.Type {
		case diffmatchpatch.DiffInsert:
			added += count
		case diffmatchpatch.DiffDelete:
			removed += count
		}
	}
	return added, removed
}

func countTextLines(value string) int64 {
	if value == "" {
		return 0
	}
	count := int64(strings.Count(value, "\n"))
	if !strings.HasSuffix(value, "\n") {
		count++
	}
	return count
}
