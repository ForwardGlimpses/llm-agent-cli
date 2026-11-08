package version

import "testing"

func TestString(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	defer func() {
		Version, Commit = oldVersion, oldCommit
	}()

	Version, Commit = "0.1.0", "dev"
	if got := String(); got != "0.1.0" {
		t.Errorf("dev build = %q, want 0.1.0", got)
	}

	Version, Commit = "0.2.0", "abc1234"
	if got := String(); got != "0.2.0 (abc1234)" {
		t.Errorf("release build = %q, want 0.2.0 (abc1234)", got)
	}

	Version, Commit = "0.2.0", ""
	if got := String(); got != "0.2.0" {
		t.Errorf("empty commit = %q, want 0.2.0", got)
	}
}
