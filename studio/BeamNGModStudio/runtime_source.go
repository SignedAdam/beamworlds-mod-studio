package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type gameSourceOptions struct {
	Path      string
	Query     string
	StartLine int
	MaxLines  int
}

type gameSourceLine struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type gameSourceResult struct {
	Lines     []gameSourceLine `json:"lines"`
	Truncated bool             `json:"truncated"`
}

// OpenRoot confines both reads and searches to the configured installation's Lua
// directory, including when a requested path contains a junction or symlink.
func readGameSource(ctx context.Context, installDir string, options gameSourceOptions) (gameSourceResult, error) {
	result := gameSourceResult{Lines: []gameSourceLine{}}
	if strings.TrimSpace(installDir) == "" {
		return result, errors.New("BeamNG game install directory is not configured")
	}
	root, err := os.OpenRoot(filepath.Join(installDir, "lua"))
	if err != nil {
		return result, err
	}
	defer root.Close()
	name := strings.ReplaceAll(options.Path, "\\", "/")
	if name == "" && options.Query != "" {
		name = "."
	}
	if !fs.ValidPath(name) {
		return result, errors.New("path must be relative to BeamNG's lua directory, without parent traversal")
	}
	if options.StartLine < 1 {
		options.StartLine = 1
	}
	if options.MaxLines <= 0 || options.MaxLines > 300 {
		options.MaxLines = 150
	}
	query := []byte(options.Query)
	buffer := make([]byte, 64<<10)
	readFile := func(name string) error {
		if !strings.EqualFold(filepath.Ext(name), ".lua") {
			return errors.New("only installed Lua source files can be read")
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("source path is not a regular file")
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(buffer, 1<<20)
		lineNumber := 0
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return err
			}
			lineNumber++
			if lineNumber < options.StartLine {
				continue
			}
			line := scanner.Bytes()
			if len(query) != 0 && !bytes.Contains(line, query) {
				continue
			}
			if len(result.Lines) == options.MaxLines {
				result.Truncated = true
				return fs.SkipAll
			}
			if len(line) > 2048 {
				end := 2048
				for end > 0 && !utf8.RuneStart(line[end]) {
					end--
				}
				line = line[:end]
				result.Truncated = true
			}
			result.Lines = append(result.Lines, gameSourceLine{Path: name, Line: lineNumber, Text: string(line)})
		}
		return scanner.Err()
	}
	if options.Query == "" {
		err = readFile(name)
	} else {
		err = fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".lua") {
				return nil
			}
			return readFile(path)
		})
	}
	if errors.Is(err, fs.SkipAll) {
		err = nil
	}
	if err != nil {
		return result, fmt.Errorf("read installed BeamNG Lua: %w", err)
	}
	return result, nil
}
