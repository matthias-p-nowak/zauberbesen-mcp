package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCurrentDirectory verifies the tool reports the process working directory.
func TestCurrentDirectory(t *testing.T) {
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	result, err := currentDirectory()
	if err != nil {
		t.Fatalf("current directory: %v", err)
	}
	if result.Text != want+"\n" {
		t.Fatalf("current directory = %q, want %q", result.Text, want+"\n")
	}
}

// TestListFiles verifies one-level and recursive deterministic listings.
func TestListFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteTestFile(t, filepath.Join(root, ".hidden"), "secret\n")
	mustWriteTestFile(t, filepath.Join(root, "nested", "file.txt"), "hello\n")

	flat, err := listFiles(listFilesInput{Path: root})
	if err != nil {
		t.Fatalf("list flat: %v", err)
	}
	if !strings.Contains(flat.Text, filepath.Join(root, ".hidden")+"\tfile\t") {
		t.Fatalf("flat listing omits hidden file: %s", flat.Text)
	}
	if strings.Contains(flat.Text, "file.txt") {
		t.Fatalf("flat listing unexpectedly recursed: %s", flat.Text)
	}

	recursive, err := listFiles(listFilesInput{Path: root, Recursive: true})
	if err != nil {
		t.Fatalf("list recursive: %v", err)
	}
	if !strings.Contains(recursive.Text, filepath.Join(root, "nested", "file.txt")) {
		t.Fatalf("recursive listing omits nested file: %s", recursive.Text)
	}

	limited, err := listFiles(listFilesInput{Path: root, Recursive: true, Limit: 1})
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if !limited.Truncated || limited.Omitted != 2 {
		t.Fatalf("limited listing = truncated:%v omitted:%d text:%q", limited.Truncated, limited.Omitted, limited.Text)
	}
	if _, err := listFiles(listFilesInput{Path: root, Limit: maxListingLimit + 1}); err == nil {
		t.Fatal("listing above maximum limit did not fail")
	}
}

// TestGrepFiles verifies regex, fixed-text, glob, and line-number behavior.
func TestGrepFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteTestFile(t, filepath.Join(root, "one.go"), "alpha\na.b\n")
	mustWriteTestFile(t, filepath.Join(root, "two.txt"), "alphabet\naXb\n")

	regex, err := grepFiles(grepInput{Path: root, Pattern: `^alpha`, Glob: "*.go"})
	if err != nil {
		t.Fatalf("regex grep: %v", err)
	}
	if !strings.Contains(regex.Text, "one.go:1:alpha") || strings.Contains(regex.Text, "two.txt") {
		t.Fatalf("unexpected regex results: %s", regex.Text)
	}

	fixed, err := grepFiles(grepInput{Path: root, Pattern: "a.b", Fixed: true})
	if err != nil {
		t.Fatalf("fixed grep: %v", err)
	}
	if !strings.Contains(fixed.Text, "one.go:2:a.b") || strings.Contains(fixed.Text, "aXb") {
		t.Fatalf("unexpected fixed results: %s", fixed.Text)
	}
}

// TestReadFile verifies numbered inclusive ranges and truncation reporting.
func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	mustWriteTestFile(t, path, "one\ntwo\nthree\n")

	result, err := readFile(readFileInput{Path: path, StartLine: 2, EndLine: 3})
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if result.Text != "2: two\n3: three\n" {
		t.Fatalf("range output = %q", result.Text)
	}

	largePath := filepath.Join(t.TempDir(), "large.txt")
	mustWriteTestFile(t, largePath, strings.Repeat("1234567890\n", 7000))
	large, err := readFile(readFileInput{Path: largePath})
	if err != nil {
		t.Fatalf("read large file: %v", err)
	}
	if !large.Truncated || large.Omitted == 0 || len(large.Text) > maxToolTextBytes {
		t.Fatalf("large result = truncated:%v omitted:%d bytes:%d", large.Truncated, large.Omitted, len(large.Text))
	}
}

// TestEditAndWriteFile verifies exact replacement and complete writes.
func TestEditAndWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.txt")
	if _, err := writeFile(writeFileInput{Path: path, Content: "before\n"}); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := editFile(editFileInput{Path: path, OldText: "before", NewText: "after"}); err != nil {
		t.Fatalf("edit file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "after\n" {
		t.Fatalf("edited content = %q", data)
	}
	if _, err := editFile(editFileInput{Path: path, OldText: "missing", NewText: "x"}); err == nil {
		t.Fatal("missing old text did not fail")
	}
}

// TestWriteLines verifies inclusive replacement, deletion, and newline state.
func TestWriteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	mustWriteTestFile(t, path, "one\ntwo\nthree\n")

	if _, err := writeLines(writeLinesInput{Path: path, StartLine: 2, EndLine: 2, NewText: "second\nextra"}); err != nil {
		t.Fatalf("replace lines: %v", err)
	}
	assertTestFile(t, path, "one\nsecond\nextra\nthree\n")

	if _, err := writeLines(writeLinesInput{Path: path, StartLine: 2, EndLine: 3, NewText: ""}); err != nil {
		t.Fatalf("delete lines: %v", err)
	}
	assertTestFile(t, path, "one\nthree\n")

	if _, err := writeLines(writeLinesInput{Path: path, StartLine: 0, EndLine: 1, NewText: "bad"}); err == nil {
		t.Fatal("invalid line range did not fail")
	}
}

// mustWriteTestFile creates parent directories and writes a test fixture.
func mustWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// assertTestFile compares a test file with its expected content.
func assertTestFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != expected {
		t.Fatalf("content of %s = %q, want %q", path, data, expected)
	}
}
