package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxToolTextBytes    = 64 * 1024
	defaultListingLimit = 1000
	maxListingLimit     = 10000
)

// textOutput is the common bounded textual result returned by filesystem tools.
type textOutput struct {
	Text      string `json:"text" jsonschema:"tool output text"`
	Truncated bool   `json:"truncated" jsonschema:"whether output exceeded the 64 KiB limit"`
	Omitted   int    `json:"omitted,omitempty" jsonschema:"entries or matches omitted after truncation"`
}

// listFilesInput describes a directory listing request.
type listFilesInput struct {
	Path      string `json:"path" jsonschema:"file or directory path to list"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"walk directories recursively"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum entries to return, defaults to 1000 and cannot exceed 10000"`
}

// grepInput describes a content search request.
type grepInput struct {
	Pattern string `json:"pattern" jsonschema:"regular expression or literal search text"`
	Path    string `json:"path" jsonschema:"file or directory to search"`
	Glob    string `json:"glob,omitempty" jsonschema:"optional filepath glob applied to searched files"`
	Fixed   bool   `json:"fixed,omitempty" jsonschema:"treat pattern as literal text"`
}

// readFileInput describes a complete or ranged file read.
type readFileInput struct {
	Path      string `json:"path" jsonschema:"file path to read"`
	StartLine int    `json:"startLine,omitempty" jsonschema:"first 1-based line to read"`
	EndLine   int    `json:"endLine,omitempty" jsonschema:"last 1-based line to read, inclusive"`
}

// editFileInput describes a unique exact-text replacement.
type editFileInput struct {
	Path    string `json:"path" jsonschema:"file path to edit"`
	OldText string `json:"oldText" jsonschema:"text that must occur exactly once"`
	NewText string `json:"newText" jsonschema:"replacement text"`
}

// writeFileInput describes a complete file write.
type writeFileInput struct {
	Path    string `json:"path" jsonschema:"file path to create or replace"`
	Content string `json:"content" jsonschema:"complete new file content"`
}

// writeLinesInput describes replacement of an existing inclusive line range.
type writeLinesInput struct {
	Path      string `json:"path" jsonschema:"file path to edit"`
	StartLine int    `json:"startLine" jsonschema:"first 1-based line to replace"`
	EndLine   int    `json:"endLine" jsonschema:"last 1-based line to replace, inclusive"`
	NewText   string `json:"newText" jsonschema:"replacement text"`
}

// boundedText accumulates UTF-8 text up to the fixed result limit.
type boundedText struct {
	builder   strings.Builder
	truncated bool
	omitted   int
}

// add appends one complete item when it fits and counts it otherwise.
func (b *boundedText) add(value string) {
	if b.truncated || b.builder.Len()+len(value) > maxToolTextBytes {
		b.truncated = true
		b.omitted++
		return
	}
	b.builder.WriteString(value)
}

// output returns the accumulated bounded result.
func (b *boundedText) output() textOutput {
	return textOutput{Text: b.builder.String(), Truncated: b.truncated, Omitted: b.omitted}
}

// registerFilesystemTools adds all accepted filesystem tools to server.
func registerFilesystemTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{Name: "current_directory", Description: "Return the server process working directory used for relative paths."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, textOutput, error) {
			output, err := currentDirectory()
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "list_files", Description: "List files without applying an application-level path restriction."},
		func(_ context.Context, _ *mcp.CallToolRequest, input listFilesInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := listFiles(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "grep", Description: "Search file contents using a regular expression or literal text."},
		func(_ context.Context, _ *mcp.CallToolRequest, input grepInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := grepFiles(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "read_file", Description: "Read a file or a 1-based inclusive line range."},
		func(_ context.Context, _ *mcp.CallToolRequest, input readFileInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := readFile(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "edit_file", Description: "Replace text that occurs exactly once in a file."},
		func(_ context.Context, _ *mcp.CallToolRequest, input editFileInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := editFile(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "write_file", Description: "Create or replace a complete file."},
		func(_ context.Context, _ *mcp.CallToolRequest, input writeFileInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := writeFile(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "write_lines", Description: "Replace an existing 1-based inclusive line range."},
		func(_ context.Context, _ *mcp.CallToolRequest, input writeLinesInput) (*mcp.CallToolResult, textOutput, error) {
			output, err := writeLines(input)
			return nil, output, err
		})
}

// currentDirectory returns the process working directory for relative paths.
func currentDirectory() (textOutput, error) {
	directory, err := os.Getwd()
	if err != nil {
		return textOutput{}, err
	}
	return textOutput{Text: directory + "\n"}, nil
}

// listFiles lists a path deterministically and includes hidden entries.
func listFiles(input listFilesInput) (textOutput, error) {
	if input.Path == "" {
		return textOutput{}, errors.New("path is required")
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultListingLimit
	}
	if limit < 1 || limit > maxListingLimit {
		return textOutput{}, fmt.Errorf("limit must be between 1 and %d", maxListingLimit)
	}
	info, err := os.Lstat(input.Path)
	if err != nil {
		return textOutput{}, err
	}
	var output boundedText
	entryCount := 0
	addEntry := func(path string, entry fs.FileInfo) {
		entryCount++
		if entryCount > limit {
			output.truncated = true
			output.omitted++
			return
		}
		kind := "file"
		switch {
		case entry.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		case entry.IsDir():
			kind = "directory"
		case !entry.Mode().IsRegular():
			kind = "other"
		}
		output.add(fmt.Sprintf("%s\t%s\t%d\n", path, kind, entry.Size()))
	}
	if !info.IsDir() {
		addEntry(input.Path, info)
		return output.output(), nil
	}
	if !input.Recursive {
		entries, readErr := os.ReadDir(input.Path)
		if readErr != nil {
			return textOutput{}, readErr
		}
		for _, entry := range entries {
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				return textOutput{}, infoErr
			}
			addEntry(filepath.Join(input.Path, entry.Name()), entryInfo)
		}
		return output.output(), nil
	}
	err = filepath.Walk(input.Path, func(path string, entry fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != input.Path {
			addEntry(path, entry)
		}
		return nil
	})
	return output.output(), err
}

// grepFiles searches regular files beneath the requested path.
func grepFiles(input grepInput) (textOutput, error) {
	if input.Path == "" || input.Pattern == "" {
		return textOutput{}, errors.New("path and pattern are required")
	}
	pattern := input.Pattern
	if input.Fixed {
		pattern = regexp.QuoteMeta(pattern)
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return textOutput{}, fmt.Errorf("invalid pattern: %w", err)
	}
	if input.Glob != "" {
		if _, err = filepath.Match(input.Glob, "candidate"); err != nil {
			return textOutput{}, fmt.Errorf("invalid glob: %w", err)
		}
	}
	rootInfo, err := os.Stat(input.Path)
	if err != nil {
		return textOutput{}, err
	}
	var output boundedText
	search := func(path string) error {
		if input.Glob != "" {
			relative, relErr := filepath.Rel(input.Path, path)
			if relErr != nil {
				return relErr
			}
			matched, matchErr := filepath.Match(input.Glob, relative)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				matched, _ = filepath.Match(input.Glob, filepath.Base(path))
			}
			if !matched {
				return nil
			}
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if matcher.MatchString(line) {
				output.add(path + ":" + strconv.Itoa(lineNumber) + ":" + line + "\n")
			}
		}
		return scanner.Err()
	}
	if rootInfo.Mode().IsRegular() {
		err = search(input.Path)
	} else if rootInfo.IsDir() {
		err = filepath.WalkDir(input.Path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() {
				return search(path)
			}
			return nil
		})
	} else {
		err = fmt.Errorf("path is not a regular file or directory: %s", input.Path)
	}
	return output.output(), err
}

// readFile reads and numbers a complete file or inclusive line range.
func readFile(input readFileInput) (textOutput, error) {
	content, err := readUTF8File(input.Path)
	if err != nil {
		return textOutput{}, err
	}
	lines, _ := splitFileLines(content)
	if len(lines) == 0 {
		return textOutput{}, nil
	}
	start := input.StartLine
	if start == 0 {
		start = 1
	}
	end := input.EndLine
	if end == 0 {
		end = len(lines)
	}
	if start < 1 || end < start || end > len(lines) {
		return textOutput{}, fmt.Errorf("line range %d-%d is outside 1-%d", start, end, len(lines))
	}
	var output boundedText
	for index := start - 1; index < end; index++ {
		output.add(strconv.Itoa(index+1) + ": " + lines[index] + "\n")
	}
	return output.output(), nil
}

// editFile performs one exact replacement while retaining file permissions.
func editFile(input editFileInput) (textOutput, error) {
	if input.OldText == "" {
		return textOutput{}, errors.New("oldText must not be empty")
	}
	content, err := readUTF8File(input.Path)
	if err != nil {
		return textOutput{}, err
	}
	count := strings.Count(content, input.OldText)
	if count != 1 {
		return textOutput{}, fmt.Errorf("oldText occurs %d times; expected exactly once", count)
	}
	if err = overwriteFile(input.Path, strings.Replace(content, input.OldText, input.NewText, 1)); err != nil {
		return textOutput{}, err
	}
	return textOutput{Text: "updated " + input.Path + "\n"}, nil
}

// writeFile creates or replaces an entire UTF-8 text file.
func writeFile(input writeFileInput) (textOutput, error) {
	if input.Path == "" {
		return textOutput{}, errors.New("path is required")
	}
	if !utf8.ValidString(input.Content) {
		return textOutput{}, errors.New("content must be valid UTF-8")
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(input.Path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return textOutput{}, err
	}
	if err := os.WriteFile(input.Path, []byte(input.Content), mode); err != nil {
		return textOutput{}, err
	}
	return textOutput{Text: "wrote " + input.Path + "\n"}, nil
}

// writeLines replaces existing lines while preserving final-newline state.
func writeLines(input writeLinesInput) (textOutput, error) {
	content, err := readUTF8File(input.Path)
	if err != nil {
		return textOutput{}, err
	}
	lines, finalNewline := splitFileLines(content)
	if input.StartLine < 1 || input.EndLine < input.StartLine || input.EndLine > len(lines) {
		return textOutput{}, fmt.Errorf("line range %d-%d is outside 1-%d", input.StartLine, input.EndLine, len(lines))
	}
	replacement := replacementLines(input.NewText)
	updated := make([]string, 0, len(lines)-(input.EndLine-input.StartLine+1)+len(replacement))
	updated = append(updated, lines[:input.StartLine-1]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[input.EndLine:]...)
	newContent := strings.Join(updated, "\n")
	if finalNewline {
		newContent += "\n"
	}
	if err = overwriteFile(input.Path, newContent); err != nil {
		return textOutput{}, err
	}
	return textOutput{Text: "updated " + input.Path + "\n"}, nil
}

// readUTF8File reads a required UTF-8 text file.
func readUTF8File(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("file is not valid UTF-8")
	}
	return string(data), nil
}

// overwriteFile replaces a file while preserving its existing permissions.
func overwriteFile(path, content string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), info.Mode().Perm())
}

// splitFileLines returns logical lines without a phantom trailing line.
func splitFileLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	finalNewline := strings.HasSuffix(content, "\n")
	if finalNewline {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), finalNewline
}

// replacementLines converts replacement text to logical lines; empty deletes.
func replacementLines(content string) []string {
	if content == "" {
		return nil
	}
	if strings.HasSuffix(content, "\n") {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n")
}
