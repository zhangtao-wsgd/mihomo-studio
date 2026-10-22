package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyAtomicAndHashFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "candidate.yaml")
	dst := filepath.Join(dir, "active", "config.yaml")
	if err := os.WriteFile(src, []byte("mode: rule\nrules:\n  - MATCH,DIRECT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := copyAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	sourceHash, err := hashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	activeHash, err := hashFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if sourceHash != activeHash {
		t.Fatalf("active hash %s differs from candidate %s", activeHash, sourceHash)
	}
}
