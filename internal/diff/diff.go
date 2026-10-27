// Package diff parses git diff output to identify changed lines for incremental mutation testing.
package diff

import (
	"go/token"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// FileName represents a file path in a diff.
type FileName string

// Change represents a contiguous range of added lines in a file, numbered as the lines of the new
// file.
type Change struct {
	StartLine int
	EndLine   int
}

// Diff maps file names to their list of changes.
type Diff map[FileName][]Change

func newDiff(files []*gitdiff.File) Diff {
	result := map[FileName][]Change{}

	for _, file := range files {
		name, changes := newChanges(file)

		result[name] = changes
	}

	return result
}

func newChanges(file *gitdiff.File) (FileName, []Change) {
	var changes []Change

	for _, fragment := range file.TextFragments {
		changes = append(changes, addedRuns(fragment)...)
	}

	return FileName(file.NewName), changes
}

// addedRuns is one Change per run of consecutive added lines of fragment. A hunk holds context
// between its additions when they lie close together (up to twice git diff's context apart), so a
// single range from its first added line would leave out the later additions and take the context
// in between for changes. The new file's line advances on context and added lines, never on a
// deleted one, which also leaves the run it falls in open: no line of the new file lies between.
func addedRuns(fragment *gitdiff.TextFragment) []Change {
	var changes []Change

	line := int(fragment.NewPosition)
	inRun := false

	for _, l := range fragment.Lines {
		switch l.Op {
		case gitdiff.OpAdd:
			if inRun {
				changes[len(changes)-1].EndLine = line
			} else {
				changes = append(changes, Change{StartLine: line, EndLine: line})
			}
			inRun = true
			line++
		case gitdiff.OpContext:
			inRun = false
			line++
		case gitdiff.OpDelete:
			// Not a line of the new file.
		}
	}

	return changes
}

// IsChanged returns true if the given position is within a changed region.
// If the diff is empty, it returns true for all positions.
func (d Diff) IsChanged(pos token.Position) bool {
	if len(d) == 0 {
		return true
	}

	fileDiff := d[FileName(pos.Filename)]

	for _, change := range fileDiff {
		if pos.Line >= change.StartLine && pos.Line <= change.EndLine {
			return true
		}
	}

	return false
}
