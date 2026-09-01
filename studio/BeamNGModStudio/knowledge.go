package main

import (
	"embed"
	"fmt"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

//go:embed knowledge/*.md
var knowledgeFiles embed.FS

type KnowledgeDocument struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func knowledgeFor(kind modkit.Kind, tags []string) ([]KnowledgeDocument, error) {
	names := []string{"baseline"}
	switch kind {
	case modkit.KindVehicle:
		names = append(names, "vehicle")
	case modkit.KindMap:
		names = append(names, "map")
	case modkit.KindUI:
		names = append(names, "ui-app")
	case modkit.KindScript:
		names = append(names, "script-mixed")
	case modkit.KindMixed:
		names = append(names, contextsFromTags(tags)...)
	}
	seen := map[string]bool{}
	documents := []KnowledgeDocument{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		content, err := knowledgeFiles.ReadFile("knowledge/" + name + ".md")
		if err != nil {
			return nil, err
		}
		title := name
		if first, _, found := strings.Cut(string(content), "\n"); found {
			title = strings.TrimSpace(strings.TrimPrefix(first, "#"))
		}
		documents = append(documents, KnowledgeDocument{ID: name, Title: title, Content: string(content)})
	}
	return documents, nil
}

func contextsFromTags(tags []string) []string {
	result := []string{}
	joined := strings.ToLower(strings.Join(tags, " "))
	if strings.Contains(joined, "vehicle") || strings.Contains(joined, "jbeam") {
		result = append(result, "vehicle")
	}
	if strings.Contains(joined, "map") || strings.Contains(joined, "level") {
		result = append(result, "map")
	}
	if strings.Contains(joined, "ui") || strings.Contains(joined, "app") {
		result = append(result, "ui-app")
	}
	if strings.Contains(joined, "script") || strings.Contains(joined, "lua") {
		result = append(result, "script-mixed")
	}
	if len(result) == 0 {
		result = []string{"vehicle", "map", "ui-app", "script-mixed"}
	}
	return result
}

func renderKnowledgePrompt(documents []KnowledgeDocument) string {
	var builder strings.Builder
	builder.WriteString("Use the following project-owned BeamNG architecture guidance. It is context, not source to copy.\n\n")
	for _, document := range documents {
		fmt.Fprintf(&builder, "<knowledge id=%q>\n%s\n</knowledge>\n\n", document.ID, document.Content)
	}
	return builder.String()
}
