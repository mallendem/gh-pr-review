package gh

import (
	"strings"
	"testing"
)

func TestParseDiffHashesPerChangeBlock(t *testing.T) {
	// Git merges hunks whose changes sit within a few lines of each other, so
	// the same bump appears twice in one hunk here...
	merged := `diff --git a/.github/workflows/publish.yml b/.github/workflows/publish.yml
--- a/.github/workflows/publish.yml
+++ b/.github/workflows/publish.yml
@@ -20,12 +20,12 @@ jobs:
       - name: Log in to the registry
-        uses: docker/login-action@af1e73f # v4.4.0
+        uses: docker/login-action@dbcb813 # v4.6.0
         with:
           registry: ghcr.io
           username: someone
-      - uses: docker/login-action@af1e73f # v4.4.0
+      - uses: docker/login-action@dbcb813 # v4.6.0
         with:
           registry: other
`
	// ...and only once, on its own, in this one.
	single := `diff --git a/deploy.yml b/deploy.yml
--- a/deploy.yml
+++ b/deploy.yml
@@ -1,3 +1,3 @@
 steps:
-      - uses: docker/login-action@af1e73f # v4.4.0
+      - uses: docker/login-action@dbcb813 # v4.6.0
   done
`

	mergedBlocks := parseDiff(merged)
	if len(mergedBlocks) != 2 {
		t.Fatalf("expected 2 change blocks in the merged hunk, got %d", len(mergedBlocks))
	}
	if mergedBlocks[0].hash != mergedBlocks[1].hash {
		t.Fatalf("the two identical bumps in one hunk hashed differently:\n%s\n%s",
			mergedBlocks[0].hash, mergedBlocks[1].hash)
	}

	singleBlocks := parseDiff(single)
	if len(singleBlocks) != 1 {
		t.Fatalf("expected 1 change block, got %d", len(singleBlocks))
	}
	if singleBlocks[0].hash != mergedBlocks[0].hash {
		t.Fatalf("the same logical change hashed differently across repos:\nmerged: %s\nsingle: %s",
			mergedBlocks[0].hash, singleBlocks[0].hash)
	}
}

func TestParseDiffSeparatesUnrelatedChanges(t *testing.T) {
	diff := `diff --git a/ci.yml b/ci.yml
@@ -1,8 +1,8 @@
 jobs:
-  image: alpine:3.19
+  image: alpine:3.20
   steps:
     - run: make
-  timeout: 10
+  timeout: 20
`
	blocks := parseDiff(diff)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks for 2 separated changes, got %d", len(blocks))
	}
	if blocks[0].hash == blocks[1].hash {
		t.Fatal("unrelated changes must not share a hash")
	}
}

func TestParseDiffTracksFilePerBlock(t *testing.T) {
	diff := `diff --git a/a.yml b/a.yml
@@ -1 +1 @@
-x: 1
+x: 2
diff --git a/b/nested/c.txt b/b/nested/c.txt
@@ -1 +1 @@
-y
+z
`
	blocks := parseDiff(diff)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].file != "a.yml" {
		t.Errorf("block 0 file = %q, want a.yml", blocks[0].file)
	}
	if blocks[1].file != "b/nested/c.txt" {
		t.Errorf("block 1 file = %q, want b/nested/c.txt", blocks[1].file)
	}
}

func TestParseDiffKeepsSurroundingContext(t *testing.T) {
	diff := `diff --git a/a.txt b/a.txt
@@ -1,5 +1,5 @@
 before one
 before two
-old
+new
 after one
`
	blocks := parseDiff(diff)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	got := strings.Join(blocks[0].raw, "\n")
	want := " before one\n before two\n-old\n+new\n after one"
	if got != want {
		t.Fatalf("raw context = %q, want %q", got, want)
	}
}

// A block's context must stop at its neighbours. Running past them made the
// changes pane show two unrelated bumps for a hash that only covers one.
func TestParseDiffContextStopsAtNeighbouringBlocks(t *testing.T) {
	diff := `diff --git a/ci.yml b/ci.yml
@@ -10,12 +10,12 @@ jobs:
     steps:
-      - uses: actions/setup-go@v5
+      - uses: actions/setup-go@v7
         with:
           go-version-file: .go-version
-      - uses: magefile/mage-action@v3
+      - uses: magefile/mage-action@v4
         with:
           install-only: true
`
	blocks := parseDiff(diff)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	first := strings.Join(blocks[0].raw, "\n")
	if strings.Contains(first, "mage-action@v4") {
		t.Errorf("block 0 context swallowed the next block's change:\n%s", first)
	}
	if !strings.Contains(first, "setup-go@v7") || !strings.Contains(first, "go-version-file") {
		t.Errorf("block 0 lost its own change or context:\n%s", first)
	}

	second := strings.Join(blocks[1].raw, "\n")
	if strings.Contains(second, "setup-go@v5") {
		t.Errorf("block 1 context swallowed the previous block's change:\n%s", second)
	}
	if !strings.Contains(second, "mage-action@v4") || !strings.Contains(second, "install-only") {
		t.Errorf("block 1 lost its own change or context:\n%s", second)
	}
	// The context between the two blocks belongs to both.
	if !strings.Contains(second, "go-version-file") {
		t.Errorf("block 1 lost the context leading up to it:\n%s", second)
	}
}

func TestParseDiffIgnoresLinesOutsideHunks(t *testing.T) {
	// "--- a/x" and "+++ b/x" precede the hunk header and must not be mistaken
	// for change lines.
	diff := `diff --git a/x b/x
index 1111111..2222222 100644
--- a/x
+++ b/x
@@ -1 +1 @@
-a
+b
`
	blocks := parseDiff(diff)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if len(blocks[0].lines) != 2 {
		t.Fatalf("expected 2 change lines, got %v", blocks[0].lines)
	}
}

func TestParseDiffEmpty(t *testing.T) {
	if blocks := parseDiff(""); len(blocks) != 0 {
		t.Fatalf("expected no blocks for an empty diff, got %d", len(blocks))
	}
}
