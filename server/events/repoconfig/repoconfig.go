package repoconfig

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	"github.com/runatlantis/atlantis/server/logging"
	"gopkg.in/yaml.v2"
)

const AtlantisYAMLFilename = "atlantis.yaml"

type AtlantisYAML struct {
	Version   int                 `yaml:"version"`
	Projects  []Project           `yaml:"projects"`
	Pipelines map[string]Pipeline `yaml:"pipelines"`
}

// ProjectAndPipeline is used by stages to actually run the pipeline for this
// project. Handles setting up defaults if not configured.
// todo: better name!
type ProjectAndPipeline struct {
	TerraformVersion  *version.Version
	ApplyRequirements []string
	Pipeline          PipelineImpl
}

type PipelineImpl struct {
	Apply []Step
	Plan  []Step
}
type ApplyRequirement interface {
	// IsMet returns true if the requirement is met and false if not.
	// If it returns false, it also returns a string describing why not.
	IsMet() (bool, string)
}

type Project struct {
	Dir               string   `yaml:"dir"`
	Workspace         string   `yaml:"workspace"`
	Pipeline          string   `yaml:"pipeline"`
	TerraformVersion  string   `yaml:"terraform_version"`
	AutoPlan          AutoPlan `yaml:"auto_plan"`
	ApplyRequirements []string `yaml:"apply_requirements"`
}

type AutoPlan struct {
	WhenModified []string `yaml:"when_modified"`
	Enabled      bool     `yaml:"enabled"` // defaults to true
}

type Pipeline struct {
	Apply Stage `yaml:"apply"`
	Plan  Stage `yaml:"plan"`
}

type Stage struct {
	Steps []Step `yaml:"steps"` // can either be a built in step like 'plan' or a custom step like 'run: echo hi'
}

type ServerSideAtlantisYAML struct {
	Version      int                 `yaml:"version"`
	Repositories []Repository        `yaml:"repositories"`
	Pipelines    map[string]Pipeline `yaml:"pipelines"`
}

type Repository struct {
	ID       string    `yaml:"id"` // if starts with '/' then is interpreted as a regular expression
	Projects []Project `yal:"projects"`
}

type PlanStage struct {
	Steps []Step
}

type ApplyStage struct {
	Steps             []Step
	ApplyRequirements []ApplyRequirement
}

func (p PlanStage) Run() (string, error) {
	var outputs string
	for _, step := range p.Steps {
		out, err := step.Run()
		if err != nil {
			return outputs, err
		}
		if out != "" {
			// Outputs are separated by newlines.
			outputs += "\n" + out
		}
	}
	return outputs, nil
}

func (a ApplyStage) Run() (string, error) {
	var outputs string
	for _, step := range a.Steps {
		out, err := step.Run()
		if err != nil {
			return outputs, err
		}
		if out != "" {
			// Outputs are separated by newlines.
			outputs += "\n" + out
		}
	}
	return outputs, nil
}

type Reader struct {
	TerraformExecutor TerraformExec
	DefaultTFVersion  *version.Version
}

func (r *Reader) BuildPlanStage(log *logging.SimpleLogger, repoDir string, workspace string, relProjectPath string, extraCommentArgs []string, username string) (*PlanStage, error) {
	// read and validate the config file
	configFile := filepath.Join(repoDir, AtlantisYAMLFilename)
	configData, err := ioutil.ReadFile(configFile)
	if err == nil {
		// If the config file exists, parse it.
		config, err := r.Parse(configData)
		if err != nil {
			return nil, err
		}
		if err := r.validateConfig(config); err != nil {
			return nil, err
		}
		// todo: finish
		return nil, err
	} else if err != nil && os.IsNotExist(err) {
		// If it doesn't exist, use defaults.
		log.Info("no %s file found–continuing with defaults", AtlantisYAMLFilename)
		meta := StepMeta{
			Log:                   log,
			Workspace:             workspace,
			AbsolutePath:          filepath.Join(repoDir, relProjectPath),
			DirRelativeToRepoRoot: relProjectPath,
			// If there's no config then we should use the default tf version.
			TerraformVersion:  r.DefaultTFVersion,
			TerraformExecutor: r.TerraformExecutor,
			ExtraCommentArgs:  extraCommentArgs,
			Username:          username,
		}
		// The default plan stage is just init and plan.
		return &PlanStage{
			Steps: []Step{
				&InitStep{
					ExtraArgs: nil,
					Meta:      meta,
				},
				&PlanStep{
					ExtraArgs: nil,
					Meta:      meta,
				},
			},
		}, nil

	} else {
		// If it does exist but we can't read it then error out.
		return nil, errors.Wrapf(err, "unable to read %s file", AtlantisYAMLFilename)
	}

	// get the config and pipeline for this project
	// initialize the steps for this stage
}
func (r *Reader) BuildApplyStage(log *logging.SimpleLogger, repoDir string, workspace string, relProjectPath string, extraCommentArgs []string, username string) (*ApplyStage, error) {
	// read and validate the config file
	configFile := filepath.Join(repoDir, AtlantisYAMLFilename)
	configData, err := ioutil.ReadFile(configFile)
	if err == nil {
		// If the config file exists, parse it.
		config, err := parse(string(configData))
		if err != nil {
			return nil, err
		}
		if err := r.validateConfig(config); err != nil {
			return nil, err
		}
		// todo: finish
		return nil, err
	} else if err != nil && os.IsNotExist(err) {
		// If it doesn't exist, use defaults.
		log.Info("no %s file found–continuing with defaults", AtlantisYAMLFilename)
		meta := StepMeta{
			Log:                   log,
			Workspace:             workspace,
			AbsolutePath:          filepath.Join(repoDir, relProjectPath),
			DirRelativeToRepoRoot: relProjectPath,
			// If there's no config then we should use the default tf version.
			TerraformVersion:  r.DefaultTFVersion,
			TerraformExecutor: r.TerraformExecutor,
			ExtraCommentArgs:  extraCommentArgs,
			Username:          username,
		}
		// The default apply stage is just an apply
		return &ApplyStage{
			Steps: []Step{
				&ApplyStep{
					ExtraArgs: nil,
					Meta:      meta,
				},
			},
			ApplyRequirements: nil,
		}, nil

	} else {
		// If it does exist but we can't read it then error out.
		return nil, errors.Wrapf(err, "unable to read %s file", AtlantisYAMLFilename)
	}
}

func (r *Reader) validateConfig(yaml AtlantisYAML) error {
	return nil
}

func (r *Reader) Parse(configData bytes) (AtlantisYAML, error) {
	yaml, err := yaml.Unmarshal()
}

type Step interface {
	// Run runs the step. It returns any output that needs to be commented back
	// onto the pull request and error.
	Run() (string, error)
}

// StepMeta is the data that is available to all steps.
type StepMeta struct {
	Log       *logging.SimpleLogger
	Workspace string
	// AbsolutePath is the path to this project on disk. It's not necessarily
	// the repository root since a project can be in a subdir of the root.
	AbsolutePath string
	// DirRelativeToRepoRoot is the directory for this project relative
	// to the repo root.
	DirRelativeToRepoRoot string
	TerraformVersion      *version.Version
	TerraformExecutor     TerraformExec
	// ExtraCommentArgs are the arguments that may have been appended to the comment.
	// For example 'atlantis plan -- -target=resource'. They are already quoted
	// further up the call tree to mitigate security issues.
	ExtraCommentArgs []string
	// VCS username of who caused this step to be executed. For example the
	// commenter, or who pushed a new commit.
	Username string
}

type TerraformExec interface {
	RunCommandWithVersion(log *logging.SimpleLogger, path string, args []string, v *version.Version, workspace string) (string, error)
}

// MustConstraint returns a constraint. It panics on error.
func MustConstraint(constraint string) version.Constraints {
	c, err := version.NewConstraint(constraint)
	if err != nil {
		panic(err)
	}
	return c
}
