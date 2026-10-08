package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGameSourceWindowAndLiteralSearch(t *testing.T) {
	t.Parallel()
	install := t.TempDir()
	lua := filepath.Join(install, "lua")
	if err := os.Mkdir(lua, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lua, "api.lua"), []byte("local M = {}\nM.a.b = true\nM.axb = false\nreturn M\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := readGameSource(context.Background(), install, gameSourceOptions{Path: "api.lua", StartLine: 2, MaxLines: 2})
	if err != nil || len(page.Lines) != 2 || page.Lines[0].Line != 2 || page.Lines[1].Line != 3 || !page.Truncated {
		t.Fatalf("wrong middle window: %#v, %v", page, err)
	}
	last, err := readGameSource(context.Background(), install, gameSourceOptions{Path: "api.lua", StartLine: 4, MaxLines: 2})
	if err != nil || len(last.Lines) != 1 || last.Lines[0].Line != 4 || last.Truncated {
		t.Fatalf("wrong final window: %#v, %v", last, err)
	}
	found, err := readGameSource(context.Background(), install, gameSourceOptions{Query: "a.b"})
	if err != nil || len(found.Lines) != 1 || found.Lines[0].Line != 2 || found.Truncated {
		t.Fatalf("search was not literal: %#v, %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readGameSource(ctx, install, gameSourceOptions{Query: "a.b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search: %v", err)
	}
}

func TestGameSourceCannotEscapeInstalledLua(t *testing.T) {
	t.Parallel()
	install := t.TempDir()
	lua := filepath.Join(install, "lua")
	if err := os.Mkdir(lua, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(install, "private.lua")
	if err := os.WriteFile(outside, []byte("private contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../private.lua", "..\\private.lua", outside} {
		if result, err := readGameSource(context.Background(), install, gameSourceOptions{Path: path}); err == nil || len(result.Lines) != 0 {
			t.Fatalf("escaped lua root through %q: %#v, %v", path, result, err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(lua, "escape.lua")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if result, err := readGameSource(context.Background(), install, gameSourceOptions{Path: "escape.lua"}); err == nil || len(result.Lines) != 0 {
			t.Fatalf("followed escaping symlink: %#v, %v", result, err)
		}
	})
}

func TestGameSourceReportsClippedLinesAndResults(t *testing.T) {
	t.Parallel()
	install := t.TempDir()
	lua := filepath.Join(install, "lua")
	if err := os.Mkdir(lua, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", 3000) + " needle\nneedle\n"
	if err := os.WriteFile(filepath.Join(lua, "api.lua"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := readGameSource(context.Background(), install, gameSourceOptions{Query: "needle", MaxLines: 1})
	if err != nil || len(result.Lines) != 1 || result.Lines[0].Line != 1 || len(result.Lines[0].Text) > 2048 || !result.Truncated {
		t.Fatalf("lost late-line match or failed to bound output: %#v, %v", result, err)
	}
}
