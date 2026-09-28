package diff

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/bluekeyes/go-gitdiff/gitdiff"

	"github.com/go-gremlins/gremlins/internal/configuration"
	"github.com/go-gremlins/gremlins/internal/log"
)

// New creates the Diff of the module rooted at moduleRoot by parsing git diff output using the
// default command executor.
func New(moduleRoot string) (Diff, error) {
	return NewWithCmd(moduleRoot, exec.Command)
}

type execCmd interface {
	CombinedOutput() ([]byte, error)
}

// NewWithCmd creates the Diff of the module rooted at moduleRoot by parsing git diff output using
// a custom command executor. It names each file from the module root, as the engine names the
// files it mutates, and leaves out the files outside the module, wherever Gremlins was started.
// This is useful for testing.
func NewWithCmd[T execCmd](moduleRoot string, cmdContext func(name string, args ...string) T) (Diff, error) {
	diffRef := configuration.Get[string](configuration.UnleashDiffRef)
	if diffRef == "" {
		return nil, nil
	}

	log.Infoln("Gathering files diff...")

	cmd := cmdContext("git", "-C", moduleRoot, "diff", "--relative", "--merge-base", diffRef)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("an error occured while calling git diff: %w\n\n%s", err, out)
	}

	files, _, err := gitdiff.Parse(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("an error occured while parsing diff: %w", err)
	}

	return newDiff(files), nil
}
