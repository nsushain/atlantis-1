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

	"github.com/runatlantis/atlantis/server/events/locking"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/repoconfig"
	"github.com/runatlantis/atlantis/server/events/run"
	"github.com/runatlantis/atlantis/server/events/terraform"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

//go:generate pegomock generate -m --use-experimental-model-gen --package mocks -o mocks/mock_lock_url_generator.go LockURLGenerator

// LockURLGenerator consumes lock URLs.
type LockURLGenerator interface {
	// SetLockURL takes a function that given a lock id, will return a url
	// to view that lock.
	SetLockURL(func(id string) (url string))
}

// PlanExecutor handles everything related to running terraform plan.
type PlanExecutor struct {
	VCSClient         vcs.ClientProxy
	Terraform         terraform.Client
	Locker            locking.Locker
	LockURL           func(id string) (url string)
	Run               run.Runner
	Workspace         AtlantisWorkspace
	ProjectPreExecute ProjectPreExecutor
	ProjectFinder     ProjectFinder
	RepoConfigReader  repoconfig.Reader
}

// PlanSuccess is the result of a successful plan.
type PlanSuccess struct {
	TerraformOutput string
	LockURL         string
}

// SetLockURL takes a function that given a lock id, will return a url
// to view that lock.
func (p *PlanExecutor) SetLockURL(f func(id string) (url string)) {
	p.LockURL = f
}

// Execute executes terraform plan for the ctx.
func (p *PlanExecutor) Execute(ctx *CommandContext) CommandResponse {
	cloneDir, err := p.Workspace.Clone(ctx.Log, ctx.BaseRepo, ctx.HeadRepo, ctx.Pull, ctx.Command.Workspace)
	if err != nil {
		return CommandResponse{Error: err}
	}

	stage, err := p.RepoConfigReader.BuildPlanStage(ctx.Log, cloneDir, ctx.Command.Workspace, ctx.Command.Dir, ctx.Command.Flags, ctx.User.Username)
	if err != nil {
		return CommandResponse{Error: err}
	}

	// check if we have the lock
	preExecute := p.ProjectPreExecute.Execute(ctx, models.NewProject(ctx.BaseRepo.FullName, ctx.Command.Dir))
	if preExecute.ProjectResult != (ProjectResult{}) {
		return CommandResponse{ProjectResults: []ProjectResult{preExecute.ProjectResult}}
	}

	out, err := stage.Run()
	if err != nil {
		// Plan failed so unlock the state.
		if _, unlockErr := p.Locker.Unlock(preExecute.LockResponse.LockKey); unlockErr != nil {
			ctx.Log.Err("error unlocking state after plan error: %v", unlockErr)
		}
		return CommandResponse{ProjectResults: []ProjectResult{{Error: fmt.Errorf("%s\n%s", err.Error(), out)}}}
	}

	return CommandResponse{ProjectResults: []ProjectResult{{
		PlanSuccess: &PlanSuccess{
			TerraformOutput: out,
			LockURL:         p.LockURL(preExecute.LockResponse.LockKey),
		},
	}}}

	// NOTE: this has been modified from how it worked before, now we expect a directory to be specified
	//if ctx.Command.Dir == "" {
	//	// If they didn't specify a directory to plan in, figure out what
	//	// projects have been modified so we know where to run plan.
	//	modifiedFiles, err := p.VCSClient.GetModifiedFiles(ctx.BaseRepo, ctx.Pull, ctx.VCSHost)
	//	if err != nil {
	//		return CommandResponse{Error: errors.Wrap(err, "getting modified files")}
	//	}
	//	ctx.Log.Info("found %d files modified in this pull request", len(modifiedFiles))
	//	projects = p.ProjectFinder.DetermineProjects(ctx.Log, modifiedFiles, ctx.BaseRepo.FullName, cloneDir)
	//	if len(projects) == 0 {
	//		return CommandResponse{Failure: "No Terraform files were modified."}
	//	}
	//} else {
	//project := models.Project{
	//	Path:         ctx.Command.Dir,
	//	RepoFullName: ctx.BaseRepo.FullName,
	//}
	//}

	// running against a single project since we will only ever run plan in one project if done via a comment
	//ctx.Log.Info("running plan for project at path %q", project.Path)
	//result := p.plan(ctx, cloneDir, project, cfg.Pipeline.Plan)
	//result.Path = project.Path
	//return CommandResponse{ProjectResults: []ProjectResult{result}}
}

//func (p *PlanExecutor) plan(ctx *CommandContext, repoDir string, project models.Project, steps []repoconfig.Step) ProjectResult {
// NOTE: this no longer runs terraform get/init
// that should be taken care of by the stages.
// This is still necessary since it does the plan locking.
//preExecute := p.ProjectPreExecute.Execute(ctx, repoDir, project)
//if preExecute.ProjectResult != (ProjectResult{}) {
//	return preExecute.ProjectResult
//}

//stepCtx := repoconfig.StepMeta{
//	Log:                   ctx.Log,
//	Workspace:             ctx.Command.Workspace,
//	AbsolutePath:          filepath.Join(repoDir, project.Path),
//	DirRelativeToRepoRoot: project.Path,
//	TerraformVersion:      p.Terraform.Version(),
//	ExtraCommentArgs:      ctx.Command.Flags,
//	Username:              ctx.User.Username,
//}

//config := preExecute.ProjectConfig
//terraformVersion := preExecute.TerraformVersion
//workspace := ctx.Command.Workspace
//
//// Run terraform plan.
//planFile := filepath.Join(repoDir, project.Path, fmt.Sprintf("%s.tfplan", workspace))
//userVar := fmt.Sprintf("%s=%s", atlantisUserTFVar, ctx.User.Username)
//planExtraArgs := config.GetExtraArguments(ctx.Command.Name.String())
//tfPlanCmd := append(append([]string{"plan", "-refresh", "-no-color", "-out", planFile, "-var", userVar}, planExtraArgs...), ctx.Command.Flags...)
//
//// Check if env/{workspace}.tfvars exist.
//envFileName := filepath.Join("env", workspace+".tfvars")
//if _, err := os.Stat(filepath.Join(repoDir, project.Path, envFileName)); err == nil {
//	tfPlanCmd = append(tfPlanCmd, "-var-file", envFileName)
//}
//output, err := p.Terraform.RunCommandWithVersion(ctx.Log, filepath.Join(repoDir, project.Path), tfPlanCmd, terraformVersion, workspace)
//if err != nil {
//	// Plan failed so unlock the state.
//	if _, unlockErr := p.Locker.Unlock(preExecute.LockResponse.LockKey); unlockErr != nil {
//		ctx.Log.Err("error unlocking state after plan error: %v", unlockErr)
//	}
//	return ProjectResult{Error: fmt.Errorf("%s\n%s", err.Error(), output)}
//}
//ctx.Log.Info("plan succeeded")
//
//// If there are post plan commands then run them.
//if len(config.PostPlan) > 0 {
//	absolutePath := filepath.Join(repoDir, project.Path)
//	_, err := p.Run.Execute(ctx.Log, config.PostPlan, absolutePath, workspace, terraformVersion, "post_plan")
//	if err != nil {
//		return ProjectResult{Error: errors.Wrap(err, "running post plan commands")}
//	}
//}
//
//return ProjectResult{
//	PlanSuccess: &PlanSuccess{
//		TerraformOutput: strings.Join(outputs, "\n"),
//		LockURL:         p.LockURL(preExecute.LockResponse.LockKey),
//	},
//}
//}
