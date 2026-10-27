package diff

import (
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

func TestDiff_IsChanged(t *testing.T) {
	tests := []struct {
		name string
		d    Diff
		pos  token.Position
		want bool
	}{
		{
			name: "must be changed on nil Diff",
			d:    nil,
			pos:  token.Position{},
			want: true,
		},
		{
			name: "must be changed on empty Diff",
			d:    map[FileName][]Change{},
			pos:  token.Position{},
			want: true,
		},
		{
			name: "must be changed if in range",
			d: map[FileName][]Change{
				"test": {{StartLine: 21, EndLine: 21}},
			},
			pos:  token.Position{Filename: "test", Line: 21},
			want: true,
		},
		{
			name: "must be unchanged if outside range",
			d: map[FileName][]Change{
				"test": {{StartLine: 21, EndLine: 21}},
			},
			pos:  token.Position{Filename: "test", Line: 22},
			want: false,
		},
		{
			name: "must be unchanged if no such file",
			d: map[FileName][]Change{
				"test": {{StartLine: 21, EndLine: 21}},
			},
			pos:  token.Position{Filename: "test1", Line: 21},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.d.IsChanged(tt.pos)
			if got != tt.want {
				t.Errorf("IsChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_newDiff(t *testing.T) {
	fragments := []*gitdiff.TextFragment{fragment(21, 1)}

	files := []*gitdiff.File{
		{
			NewName:       "test1",
			TextFragments: fragments,
		},
		{
			NewName:       "test2",
			TextFragments: fragments,
		},
	}

	expected := Diff{
		"test1": {{StartLine: 25, EndLine: 25}},
		"test2": {{StartLine: 25, EndLine: 25}},
	}

	result := newDiff(files)
	if !reflect.DeepEqual(result, expected) {
		t.Log("want", expected)
		t.Log("got", result)
		t.Fatalf("unexpected newDiff result")
	}
}

func Test_newChanges(t *testing.T) {
	fragments := []*gitdiff.TextFragment{
		fragment(0, 1),
		fragment(10, 0),
		fragment(21, 2),
		fragment(44, 4),
		fragment(231, 201),
	}
	file := &gitdiff.File{
		NewName:       "test",
		TextFragments: fragments,
	}

	expect := []Change{
		{StartLine: 4, EndLine: 4},
		{StartLine: 25, EndLine: 26},
		{StartLine: 48, EndLine: 51},
		{StartLine: 235, EndLine: 435},
	}

	name, changes := newChanges(file)

	if name != "test" {
		t.Fatalf("name %s unexpected", name)
	}
	if !reflect.DeepEqual(changes, expect) {
		t.Log("want", expect)
		t.Log("got", changes)
		t.Fatalf("unexpected newChanges result")
	}
}

// addedRunCases are git diffs of one file each, with the ranges newChanges must find in them: one
// per run of added lines, numbered as the lines of the new file, so that a hunk's context lines,
// within it as well as around it, are never taken for changes and no added line is left out.
var addedRunCases = []struct {
	name string
	diff string
	want []Change
}{
	{
		name: "one addition",
		diff: "@@ -1,6 +1,7 @@\n c1\n c2\n c3\n+a4\n c4\n c5\n c6\n",
		want: []Change{{StartLine: 4, EndLine: 4}},
	},
	{
		name: "two additions separated by context",
		diff: "@@ -3,6 +3,8 @@\n c3\n c4\n c5\n+a6\n c6\n+a8\n c7\n c8\n",
		want: []Change{{StartLine: 6, EndLine: 6}, {StartLine: 8, EndLine: 8}},
	},
	{
		name: "runs of several additions separated by context",
		diff: "@@ -10,4 +10,8 @@\n c10\n+a11\n+a12\n c11\n c12\n+a15\n+a16\n c13\n",
		want: []Change{{StartLine: 11, EndLine: 12}, {StartLine: 15, EndLine: 16}},
	},
	{
		name: "an addition after a deletion",
		diff: "@@ -1,6 +1,6 @@\n c1\n+a2\n c2\n-d3\n-d4\n+a4\n c5\n c6\n",
		want: []Change{{StartLine: 2, EndLine: 2}, {StartLine: 4, EndLine: 4}},
	},
	{
		name: "deletions only",
		diff: "@@ -2,5 +2,4 @@\n c2\n c3\n-d4\n c5\n c6\n",
		want: nil,
	},
	{
		name: "a deletion-only hunk before an addition's hunk",
		diff: "@@ -2,3 +2,2 @@\n c2\n-d3\n c4\n@@ -20,2 +19,3 @@\n c20\n+a20\n c21\n",
		want: []Change{{StartLine: 20, EndLine: 20}},
	},
	{
		name: "a file's first and last line",
		diff: "@@ -1,3 +1,5 @@\n+a1\n c1\n c2\n c3\n+a5\n",
		want: []Change{{StartLine: 1, EndLine: 1}, {StartLine: 5, EndLine: 5}},
	},
	{
		name: "a last line that had no newline at the end of the file",
		diff: "@@ -1,2 +1,2 @@\n c1\n-d2\n\\ No newline at end of file\n+a2\n",
		want: []Change{{StartLine: 2, EndLine: 2}},
	},
	{
		name: "a new file",
		diff: "@@ -0,0 +1,3 @@\n+n1\n+n2\n+n3\n",
		want: []Change{{StartLine: 1, EndLine: 3}},
	},
}

func TestNewChangesRangesEachRunOfAddedLines(t *testing.T) {
	for _, tc := range addedRunCases {
		t.Run(tc.name, func(t *testing.T) {
			header := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n"
			if strings.HasPrefix(tc.diff, "@@ -0,0 ") {
				header = "diff --git a/a.go b/a.go\nnew file mode 100644\n--- /dev/null\n+++ b/a.go\n"
			}
			files, _, err := gitdiff.Parse(strings.NewReader(header + tc.diff))
			if err != nil || len(files) != 1 {
				t.Fatalf("the case's diff does not parse as one file: %d files, %v", len(files), err)
			}

			name, changes := newChanges(files[0])

			if name != "a.go" {
				t.Errorf("name %q, want a.go", name)
			}
			if !reflect.DeepEqual(changes, tc.want) {
				t.Errorf("changes %v, want %v", changes, tc.want)
			}
		})
	}
}

func fragment(startLine int, adds int, del ...int) *gitdiff.TextFragment {
	const contexts = 4

	dels := adds
	if len(del) > 0 {
		dels = del[0]
	}

	var lines []gitdiff.Line

	lines = append(lines, opLines(gitdiff.OpContext, contexts)...)
	lines = append(lines, opLines(gitdiff.OpDelete, dels)...)
	lines = append(lines, opLines(gitdiff.OpAdd, adds)...)
	lines = append(lines, opLines(gitdiff.OpContext, contexts)...)

	line := int64(startLine)
	added := int64(adds)
	deleted := int64(dels)

	return &gitdiff.TextFragment{
		OldLines:        line - 1,
		NewPosition:     line,
		LinesAdded:      added,
		LinesDeleted:    deleted,
		LeadingContext:  contexts,
		TrailingContext: contexts,
		Lines:           lines,
	}
}

func opLines(op gitdiff.LineOp, count int) []gitdiff.Line {
	result := make([]gitdiff.Line, count)

	for i := 0; i < count; i++ {
		result[i] = gitdiff.Line{Op: op, Line: "test"}
	}

	return result
}
