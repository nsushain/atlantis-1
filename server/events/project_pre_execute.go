// Copyright 2017 HootSuite Media Inc.
//
// Licensed under the Apache License, Version 2.0 (the License);
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an AS IS BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Modified hereafter by contributors to runatlantis/atlantis.
//
package events

import (
	"fmt"

	"github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	"github.com/runatlantis/atlantis/server/events/locking"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/run"
	"github.com/runatlantis/atlantis/server/events/terraform"
)

//go:generate pegomock generate -m --use-experimental-model-gen --package mocks -o mocks/mock_project_pre_executor.go ProjectPreExecutor

// ProjectPreExecutor executes before the plan and apply executors. It handles
// the setup tasks that are common to both plan and apply.
type ProjectPreExecutor interface {
	// Execute executes the pre plan/apply tasks.
	Execute(ctx *CommandContext, project models.Project) PreExecuteResult
}

// DefaultProjectPreExecutor implements ProjectPreExecutor.
type DefaultProjectPreExecutor struct {
	Locker       locking.Locker
	ConfigReader ProjectConfigReader
	Terraform    terraform.Client
	Run          run.Runner
}

// PreExecuteResult is the result of running the pre execute.
type PreExecuteResult struct {
	ProjectResult    ProjectResult
	ProjectConfig    ProjectConfig
	TerraformVersion *version.Version
	LockResponse     locking.TryLockResponse
}

// Execute executes the pre plan/apply tasks.
func (p *DefaultProjectPreExecutor) Execute(ctx *CommandContext, project models.Project) PreExecuteResult {
	workspace := ctx.Command.Workspace
	lockAttempt, err := p.Locker.TryLock(project, workspace, ctx.Pull, ctx.User)
	if err != nil {
		return PreExecuteResult{ProjectResult: ProjectResult{Error: errors.Wrap(err, "acquiring lock")}}
	}
	if !lockAttempt.LockAcquired && lockAttempt.CurrLock.Pull.Num != ctx.Pull.Num {
		return PreExecuteResult{ProjectResult: ProjectResult{Failure: fmt.Sprintf(
			"This project is currently locked by #%d. The locking plan must be applied or discarded before future plans can execute.",
			lockAttempt.CurrLock.Pull.Num)}}
	}
	ctx.Log.Info("acquired lock with id %q", lockAttempt.LockKey)
	//config, tfVersion, err := p.executeWithLock(ctx, repoDir, project)
	if err != nil {
		p.Locker.Unlock(lockAttempt.LockKey) // nolint: errcheck
		return PreExecuteResult{ProjectResult: ProjectResult{Error: err}}
	}
	return PreExecuteResult{ProjectConfig: ProjectConfig{}, TerraformVersion: nil, LockResponse: lockAttempt}
}

// executeWithLock executes the pre plan/apply tasks after the lock has been
// acquired. This helper func makes revoking the lock on error easier.
// Returns the project config, terraform version, or an error.
//func (p *DefaultProjectPreExecutor) executeWithLock(ctx *CommandContext, repoDir string, project models.Project) (ProjectConfig, *version.Version, error) {
//workspace := ctx.Command.Workspace

// Check if config file is found, if not we continue the run.
//var config ProjectConfig
//absolutePath := filepath.Join(repoDir, project.Path)
//if p.ConfigReader.HasLegacyConfig(absolutePath) {
//	ctx.Log.Err("found legacy atlantis.yaml in %q", absolutePath)
//	// todo: implement a converter that prints what the config should look like
//	return config, nil, errors.New("This version of Atlantis only supports atlantis.yaml version 2. If you're using version 2, ensure you have 'version: 2' at the top of your config file. Otherwise, see our docs for how to migrate to version 2 (it's really easy).")
//}

//Check if terraform version is >= 0.9.0.
//terraformVersion := p.Terraform.Version()
//if config.TerraformVersion != nil {
//	terraformVersion = config.TerraformVersion
//}
//constraints, _ := version.NewConstraint(">= 0.9.0")
//if constraints.Check(terraformVersion) {
//	ctx.Log.Info("determined that we are running terraform with version >= 0.9.0. Running version %s", terraformVersion)
//	if len(config.PreInit) > 0 {
//		_, err := p.Run.Execute(ctx.Log, config.PreInit, absolutePath, workspace, terraformVersion, "pre_init")
//		if err != nil {
//			return config, nil, errors.Wrapf(err, "running %s commands", "pre_init")
//		}
//	}
//	_, err := p.Terraform.Init(ctx.Log, absolutePath, workspace, config.GetExtraArguments("init"), terraformVersion)
//	if err != nil {
//		return config, nil, err
//	}
//} else {
//	ctx.Log.Info("determined that we are running terraform with version < 0.9.0. Running version %s", terraformVersion)
//	if len(config.PreGet) > 0 {
//		_, err := p.Run.Execute(ctx.Log, config.PreGet, absolutePath, workspace, terraformVersion, "pre_get")
//		if err != nil {
//			return config, nil, errors.Wrapf(err, "running %s commands", "pre_get")
//		}
//	}
//	terraformGetCmd := append([]string{"get", "-no-color"}, config.GetExtraArguments("get")...)
//	_, err := p.Terraform.RunCommandWithVersion(ctx.Log, absolutePath, terraformGetCmd, terraformVersion, workspace)
//	if err != nil {
//		return config, nil, err
//	}
//}
//
//stage := fmt.Sprintf("pre_%s", strings.ToLower(ctx.Command.Name.String()))
//var commands []string
//if ctx.Command.Name == Plan {
//	commands = config.PrePlan
//} else {
//	commands = config.PreApply
//}
//if len(commands) > 0 {
//	_, err := p.Run.Execute(ctx.Log, commands, absolutePath, workspace, terraformVersion, stage)
//	if err != nil {
//		return config, nil, errors.Wrapf(err, "running %s commands", stage)
//	}
//}
//return config, terraformVersion, nil
//}
