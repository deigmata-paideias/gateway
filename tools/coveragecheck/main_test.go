package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoverage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "coverage.out")
	data := []byte("mode: atomic\nexample.go:1.1,2.1 3 1\nexample.go:3.1,4.1 1 0\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := coverage(path)
	if err != nil || got != 75 {
		t.Fatalf("coverage() = %v, %v", got, err)
	}
}

func TestCoverageErrors(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"wrong\n",
		"mode: atomic\nbad\n",
		"mode: atomic\nexample.go:1.1,2.1 bad 1\n",
		"mode: atomic\nexample.go:1.1,2.1 1 bad\n",
		"mode: atomic\n",
	}
	for index, data := range tests {
		path := filepath.Join(t.TempDir(), "invalid.out")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := coverage(path); err == nil {
			t.Errorf("case %d 应失败", index)
		}
	}
	if _, err := coverage(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing profile 应失败")
	}
}
