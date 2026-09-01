package main

import (
	"strings"
	"testing"
)

func TestRuntimeSeverityHonorsStructuredLogLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want string
	}{
		{"8.9|D|engine::ShaderGen| Failed to remove a temporary file", "info"},
		{"3.1|E|OnlineServiceProvider| Could not initialize provider", "error"},
		{"4.2|W|GELua| extension is slow", "warning"},
		{"unstructured stack traceback from extension", "error"},
		{"warning: unstructured compatibility message", "warning"},
	}
	for _, test := range tests {
		if got := runtimeSeverity(test.line, strings.ToLower(test.line)); got != test.want {
			t.Errorf("runtimeSeverity(%q) = %q, want %q", test.line, got, test.want)
		}
	}
}
