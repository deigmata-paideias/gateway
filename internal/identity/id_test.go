package identity

import (
	"strings"
	"testing"
	"time"
)

func TestNewAndTimestamp(t *testing.T) {
	t.Parallel()

	before := time.Now().Add(-time.Second).UTC()
	id, err := New("req_")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !strings.HasPrefix(id, "req_") || id != strings.ToLower(id) {
		t.Fatalf("New() = %q", id)
	}
	timestamp, err := Timestamp(id)
	if err != nil {
		t.Fatalf("Timestamp() error = %v", err)
	}
	if timestamp.Before(before) || timestamp.After(time.Now().Add(time.Second)) {
		t.Fatalf("Timestamp() = %v", timestamp)
	}

	for _, invalid := range []string{"", "missing", "req_", "req_invalid!", "req_00"} {
		if _, err := Timestamp(invalid); err == nil {
			t.Errorf("Timestamp(%q) 应返回错误", invalid)
		}
	}
}

func TestNewProducesDistinctIDs(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 100)
	for range 100 {
		id, err := New("aud_")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("生成重复 ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
