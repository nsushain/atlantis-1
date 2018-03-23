package repoconfig_test

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock"
	"github.com/runatlantis/atlantis/server/events/mocks/matchers"
	"github.com/runatlantis/atlantis/server/events/repoconfig"
	matchers2 "github.com/runatlantis/atlantis/server/events/run/mocks/matchers"
	"github.com/runatlantis/atlantis/server/events/terraform/mocks"
	. "github.com/runatlantis/atlantis/testing"
)

func TestRun_NoDir(t *testing.T) {
	s := repoconfig.ApplyStep{
		Meta: repoconfig.StepMeta{
			Workspace:             "workspace",
			AbsolutePath:          "nonexistent/path",
			DirRelativeToRepoRoot: ".",
			TerraformVersion:      nil,
			ExtraCommentArgs:      nil,
			Username:              "username",
		},
	}
	_, err := s.Run()
	ErrEquals(t, "no plan found at path \".\" and workspace \"workspace\"–did you run plan?", err)
}

func TestRun_NoPlanFile(t *testing.T) {
	tmpDir, cleanup := tmpDir_stepTests(t)
	defer cleanup()

	s := repoconfig.ApplyStep{
		Meta: repoconfig.StepMeta{
			Workspace:             "workspace",
			AbsolutePath:          tmpDir,
			DirRelativeToRepoRoot: ".",
			TerraformVersion:      nil,
			ExtraCommentArgs:      nil,
			Username:              "username",
		},
	}
	_, err := s.Run()
	ErrEquals(t, "no plan found at path \".\" and workspace \"workspace\"–did you run plan?", err)
}

func TestRun_Success(t *testing.T) {
	tmpDir, cleanup := tmpDir_stepTests(t)
	defer cleanup()
	planPath := filepath.Join(tmpDir, "workspace.tfplan")
	err := ioutil.WriteFile(planPath, nil, 0644)
	Ok(t, err)

	RegisterMockTestingT(t)
	terraform := mocks.NewMockClient()

	tfVersion, _ := version.NewVersion("0.11.4")
	s := repoconfig.ApplyStep{
		Meta: repoconfig.StepMeta{
			Workspace:             "workspace",
			AbsolutePath:          tmpDir,
			DirRelativeToRepoRoot: ".",
			TerraformVersion:      tfVersion,
			ExtraCommentArgs:      []string{"comment", "args"},
			Username:              "username",
		},
		ExtraArgs:         []string{"extra", "args"},
		TerraformExecutor: terraform,
	}

	When(terraform.RunCommandWithVersion(matchers.AnyPtrToLoggingSimpleLogger(), AnyString(), AnyStringSlice(), matchers2.AnyPtrToGoVersionVersion(), AnyString())).
		ThenReturn("output", nil)
	output, err := s.Run()
	Ok(t, err)
	Equals(t, "output", output)
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(nil, tmpDir, []string{"apply", "-no-color", "extra", "args", "comment", "args", planPath}, tfVersion, "workspace")
}

// tmpDir_stepTests creates a temporary directory and returns its path along
// with a cleanup function to be called via defer.
func tmpDir_stepTests(t *testing.T) (string, func()) {
	tmpDir, err := ioutil.TempDir("", "")
	Ok(t, err)
	return tmpDir, func() { os.RemoveAll(tmpDir) }
}
