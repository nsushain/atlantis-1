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
// Package server handles the web server and executing commands that come in
// via webhooks.
package server

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/elazarl/go-bindata-assetfs"
	"github.com/gorilla/mux"
	"github.com/lkysow/go-gitlab"
	"github.com/pkg/errors"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/locking"
	"github.com/runatlantis/atlantis/server/events/locking/boltdb"
	"github.com/runatlantis/atlantis/server/events/repoconfig"
	"github.com/runatlantis/atlantis/server/events/run"
	"github.com/runatlantis/atlantis/server/events/terraform"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/events/webhooks"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/static"
	"github.com/urfave/cli"
	"github.com/urfave/negroni"
)

const LockRouteName = "lock-detail"

// Server runs the Atlantis web server.
type Server struct {
	AtlantisVersion    string
	Router             *mux.Router
	Port               int
	CommandHandler     *events.CommandHandler
	Logger             *logging.SimpleLogger
	Locker             locking.Locker
	AtlantisURL        string
	EventsController   *EventsController
	IndexTemplate      TemplateWriter
	LockDetailTemplate TemplateWriter
	SSLCertFile        string
	SSLKeyFile         string
}

// UserConfig holds config values passed in by the user.
// The mapstructure tags correspond to flags in cmd/server.go and are used when
// the config is parsed from a YAML file.
type UserConfig struct {
	AllowForkPRs        bool   `mapstructure:"allow-fork-prs"`
	AtlantisURL         string `mapstructure:"atlantis-url"`
	DataDir             string `mapstructure:"data-dir"`
	GithubHostname      string `mapstructure:"gh-hostname"`
	GithubToken         string `mapstructure:"gh-token"`
	GithubUser          string `mapstructure:"gh-user"`
	GithubWebHookSecret string `mapstructure:"gh-webhook-secret"`
	GitlabHostname      string `mapstructure:"gitlab-hostname"`
	GitlabToken         string `mapstructure:"gitlab-token"`
	GitlabUser          string `mapstructure:"gitlab-user"`
	GitlabWebHookSecret string `mapstructure:"gitlab-webhook-secret"`
	LogLevel            string `mapstructure:"log-level"`
	Port                int    `mapstructure:"port"`
	RepoWhitelist       string `mapstructure:"repo-whitelist"`
	// RequireApproval is whether to require pull request approval before
	// allowing terraform apply's to be run.
	RequireApproval bool            `mapstructure:"require-approval"`
	SlackToken      string          `mapstructure:"slack-token"`
	SSLCertFile     string          `mapstructure:"ssl-cert-file"`
	SSLKeyFile      string          `mapstructure:"ssl-key-file"`
	Webhooks        []WebhookConfig `mapstructure:"webhooks"`
}

// Config holds config for server that isn't passed in by the user.
type Config struct {
	AllowForkPRsFlag string
	AtlantisVersion  string
}

// WebhookConfig is nested within UserConfig. It's used to configure webhooks.
type WebhookConfig struct {
	// Event is the type of event we should send this webhook for, ex. apply.
	Event string `mapstructure:"event"`
	// WorkspaceRegex is a regex that is used to match against the workspace
	// that is being modified for this event. If the regex matches, we'll
	// send the webhook, ex. "production.*".
	WorkspaceRegex string `mapstructure:"workspace-regex"`
	// Kind is the type of webhook we should send, ex. slack.
	Kind string `mapstructure:"kind"`
	// Channel is the channel to send this webhook to. It only applies to
	// slack webhooks. Should be without '#'.
	Channel string `mapstructure:"channel"`
}

// NewServer returns a new server. If there are issues starting the server or
// its dependencies an error will be returned. This is like the main() function
// for the server CLI command because it injects all the dependencies.
func NewServer(userConfig UserConfig, config Config) (*Server, error) {
	var supportedVCSHosts []vcs.Host
	var githubClient *vcs.GithubClient
	var gitlabClient *vcs.GitlabClient
	if userConfig.GithubUser != "" {
		supportedVCSHosts = append(supportedVCSHosts, vcs.Github)
		var err error
		githubClient, err = vcs.NewGithubClient(userConfig.GithubHostname, userConfig.GithubUser, userConfig.GithubToken)
		if err != nil {
			return nil, err
		}
	}
	if userConfig.GitlabUser != "" {
		supportedVCSHosts = append(supportedVCSHosts, vcs.Gitlab)
		gitlabClient = &vcs.GitlabClient{
			Client: gitlab.NewClient(nil, userConfig.GitlabToken),
		}
		// If not using gitlab.com we need to set the URL to the API.
		if userConfig.GitlabHostname != "gitlab.com" {
			// Check if they've also provided a scheme so we don't prepend it
			// again.
			scheme := "https"
			schemeSplit := strings.Split(userConfig.GitlabHostname, "://")
			if len(schemeSplit) > 1 {
				scheme = schemeSplit[0]
				userConfig.GitlabHostname = schemeSplit[1]
			}
			apiURL := fmt.Sprintf("%s://%s/api/v4/", scheme, userConfig.GitlabHostname)
			if err := gitlabClient.Client.SetBaseURL(apiURL); err != nil {
				return nil, errors.Wrapf(err, "setting GitLab API URL: %s", apiURL)
			}
		}
	}
	var webhooksConfig []webhooks.Config
	for _, c := range userConfig.Webhooks {
		config := webhooks.Config{
			Channel:        c.Channel,
			Event:          c.Event,
			Kind:           c.Kind,
			WorkspaceRegex: c.WorkspaceRegex,
		}
		webhooksConfig = append(webhooksConfig, config)
	}
	webhooksManager, err := webhooks.NewMultiWebhookSender(webhooksConfig, webhooks.NewSlackClient(userConfig.SlackToken))
	if err != nil {
		return nil, errors.Wrap(err, "initializing webhooks")
	}
	vcsClient := vcs.NewDefaultClientProxy(githubClient, gitlabClient)
	commitStatusUpdater := &events.DefaultCommitStatusUpdater{Client: vcsClient}
	terraformClient, err := terraform.NewClient(userConfig.DataDir)
	// The flag.Lookup call is to detect if we're running in a unit test. If we
	// are, then we don't error out because we don't have/want terraform
	// installed on our CI system where the unit tests run.
	if err != nil && flag.Lookup("test.v") == nil {
		return nil, errors.Wrap(err, "initializing terraform")
	}
	markdownRenderer := &events.MarkdownRenderer{}
	boltdb, err := boltdb.New(userConfig.DataDir)
	if err != nil {
		return nil, err
	}
	lockingClient := locking.NewClient(boltdb)
	run := &run.Run{}
	configReader := &events.ProjectConfigManager{}
	workspaceLocker := events.NewDefaultAtlantisWorkspaceLocker()
	workspace := &events.FileWorkspace{
		DataDir: userConfig.DataDir,
	}
	projectPreExecute := &events.DefaultProjectPreExecutor{
		Locker:       lockingClient,
		Run:          run,
		ConfigReader: configReader,
		Terraform:    terraformClient,
	}
	repoConfig := repoconfig.Reader{
		DefaultTFVersion:  terraformClient.Version(),
		TerraformExecutor: terraformClient,
	}
	applyExecutor := &events.ApplyExecutor{
		VCSClient:         vcsClient,
		Terraform:         terraformClient,
		RequireApproval:   userConfig.RequireApproval,
		Run:               run,
		AtlantisWorkspace: workspace,
		ProjectPreExecute: projectPreExecute,
		RepoConfigReader:  repoConfig,
		Webhooks:          webhooksManager,
	}
	planExecutor := &events.PlanExecutor{
		VCSClient:         vcsClient,
		Terraform:         terraformClient,
		Run:               run,
		Workspace:         workspace,
		ProjectPreExecute: projectPreExecute,
		Locker:            lockingClient,
		ProjectFinder:     &events.DefaultProjectFinder{},
		RepoConfigReader:  repoConfig,
	}
	pullClosedExecutor := &events.PullClosedExecutor{
		VCSClient: vcsClient,
		Locker:    lockingClient,
		Workspace: workspace,
	}
	logger := logging.NewSimpleLogger("server", nil, false, logging.ToLogLevel(userConfig.LogLevel))
	eventParser := &events.EventParser{
		GithubUser:  userConfig.GithubUser,
		GithubToken: userConfig.GithubToken,
		GitlabUser:  userConfig.GitlabUser,
		GitlabToken: userConfig.GitlabToken,
	}
	commentParser := &events.CommentParser{
		GithubUser:  userConfig.GithubUser,
		GithubToken: userConfig.GithubToken,
		GitlabUser:  userConfig.GitlabUser,
		GitlabToken: userConfig.GitlabToken,
	}
	commandHandler := &events.CommandHandler{
		ApplyExecutor:            applyExecutor,
		PlanExecutor:             planExecutor,
		LockURLGenerator:         planExecutor,
		EventParser:              eventParser,
		VCSClient:                vcsClient,
		GithubPullGetter:         githubClient,
		GitlabMergeRequestGetter: gitlabClient,
		CommitStatusUpdater:      commitStatusUpdater,
		AtlantisWorkspaceLocker:  workspaceLocker,
		MarkdownRenderer:         markdownRenderer,
		Logger:                   logger,
		AllowForkPRs:             userConfig.AllowForkPRs,
		AllowForkPRsFlag:         config.AllowForkPRsFlag,
	}
	repoWhitelist := &events.RepoWhitelist{
		Whitelist: userConfig.RepoWhitelist,
	}
	eventsController := &EventsController{
		CommandRunner:          commandHandler,
		PullCleaner:            pullClosedExecutor,
		Parser:                 eventParser,
		CommentParser:          commentParser,
		Logger:                 logger,
		GithubWebHookSecret:    []byte(userConfig.GithubWebHookSecret),
		GithubRequestValidator: &DefaultGithubRequestValidator{},
		GitlabRequestParser:    &DefaultGitlabRequestParser{},
		GitlabWebHookSecret:    []byte(userConfig.GitlabWebHookSecret),
		RepoWhitelist:          repoWhitelist,
		SupportedVCSHosts:      supportedVCSHosts,
		VCSClient:              vcsClient,
	}
	router := mux.NewRouter()
	return &Server{
		AtlantisVersion:    config.AtlantisVersion,
		Router:             router,
		Port:               userConfig.Port,
		CommandHandler:     commandHandler,
		Logger:             logger,
		Locker:             lockingClient,
		AtlantisURL:        userConfig.AtlantisURL,
		EventsController:   eventsController,
		IndexTemplate:      indexTemplate,
		LockDetailTemplate: lockTemplate,
		SSLKeyFile:         userConfig.SSLKeyFile,
		SSLCertFile:        userConfig.SSLCertFile,
	}, nil
}

// Start creates the routes and starts serving traffic.
func (s *Server) Start() error {
	s.Router.HandleFunc("/", s.Index).Methods("GET").MatcherFunc(func(r *http.Request, rm *mux.RouteMatch) bool {
		return r.URL.Path == "/" || r.URL.Path == "/index.html"
	})
	s.Router.PathPrefix("/static/").Handler(http.FileServer(&assetfs.AssetFS{Asset: static.Asset, AssetDir: static.AssetDir, AssetInfo: static.AssetInfo}))
	s.Router.HandleFunc("/events", s.postEvents).Methods("POST")
	s.Router.HandleFunc("/locks", s.DeleteLockRoute).Methods("DELETE").Queries("id", "{id:.*}")
	lockRoute := s.Router.HandleFunc("/lock", s.GetLockRoute).Methods("GET").Queries("id", "{id}").Name(LockRouteName)
	// function that planExecutor can use to construct detail view url
	// injecting this here because this is the earliest routes are created
	s.CommandHandler.SetLockURL(func(lockID string) string {
		// ignoring error since guaranteed to succeed if "id" is specified
		u, _ := lockRoute.URL("id", url.QueryEscape(lockID))
		return s.AtlantisURL + u.RequestURI()
	})
	n := negroni.New(&negroni.Recovery{
		Logger:     log.New(os.Stdout, "", log.LstdFlags),
		PrintStack: false,
		StackAll:   false,
		StackSize:  1024 * 8,
	}, NewRequestLogger(s.Logger))
	n.UseHandler(s.Router)

	// Ensure server gracefully drains connections when stopped.
	stop := make(chan os.Signal, 1)
	// Stop on SIGINTs and SIGTERMs.
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	server := &http.Server{Addr: fmt.Sprintf(":%d", s.Port), Handler: n}
	go func() {
		s.Logger.Warn("Atlantis started - listening on port %v", s.Port)

		var err error
		if s.SSLCertFile != "" && s.SSLKeyFile != "" {
			err = server.ListenAndServeTLS(s.SSLCertFile, s.SSLKeyFile)
		} else {
			err = server.ListenAndServe()
		}

		if err != nil {
			// When shutdown safely, there will be no error.
			s.Logger.Err(err.Error())
		}
	}()
	<-stop

	s.Logger.Warn("Received interrupt. Safely shutting down")
	ctx, _ := context.WithTimeout(context.Background(), 5*time.Second) // nolint: vet
	if err := server.Shutdown(ctx); err != nil {
		return cli.NewExitError(fmt.Sprintf("while shutting down: %s", err), 1)
	}
	return nil
}

// Index is the / route.
func (s *Server) Index(w http.ResponseWriter, _ *http.Request) {
	locks, err := s.Locker.List()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "Could not retrieve locks: %s", err)
		return
	}

	var lockResults []LockIndexData
	for id, v := range locks {
		lockURL, _ := s.Router.Get(LockRouteName).URL("id", url.QueryEscape(id))
		lockResults = append(lockResults, LockIndexData{
			LockURL:      lockURL.String(),
			RepoFullName: v.Project.RepoFullName,
			PullNum:      v.Pull.Num,
			Time:         v.Time,
		})
	}
	// nolint: errcheck
	s.IndexTemplate.Execute(w, IndexData{
		Locks:           lockResults,
		AtlantisVersion: s.AtlantisVersion,
	})
}

// GetLockRoute is the GET /locks/{id} route. It renders the lock detail view.
func (s *Server) GetLockRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := mux.Vars(r)["id"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "No lock id in request")
		return
	}
	s.GetLock(w, r, id)
}

// GetLock handles a lock detail page view. getLockRoute is expected to
// be called before. This function was extracted to make it testable.
func (s *Server) GetLock(w http.ResponseWriter, _ *http.Request, id string) {
	idUnencoded, err := url.QueryUnescape(id)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "Invalid lock id")
		return
	}

	lock, err := s.Locker.GetLock(idUnencoded)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, err.Error())
		return
	}
	if lock == nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "No lock found at that id")
		return
	}

	// Extract the repo owner and repo name.
	repo := strings.Split(lock.Project.RepoFullName, "/")

	l := LockDetailData{
		LockKeyEncoded:  id,
		LockKey:         idUnencoded,
		RepoOwner:       repo[0],
		RepoName:        repo[1],
		PullRequestLink: lock.Pull.URL,
		LockedBy:        lock.Pull.Author,
		Workspace:       lock.Workspace,
		AtlantisVersion: s.AtlantisVersion,
	}

	s.LockDetailTemplate.Execute(w, l) // nolint: errcheck
}

// DeleteLockRoute handles deleting the lock at id.
func (s *Server) DeleteLockRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := mux.Vars(r)["id"]
	if !ok || id == "" {
		s.respond(w, logging.Warn, http.StatusBadRequest, "No lock id in request")
		return
	}
	s.DeleteLock(w, r, id)
}

// DeleteLock deletes the lock. DeleteLockRoute should be called first.
// This method is split out to make this route testable.
func (s *Server) DeleteLock(w http.ResponseWriter, _ *http.Request, id string) {
	idUnencoded, err := url.PathUnescape(id)
	if err != nil {
		s.respond(w, logging.Warn, http.StatusBadRequest, "Invalid lock id: %s", err)
		return
	}
	lock, err := s.Locker.Unlock(idUnencoded)
	if err != nil {
		s.respond(w, logging.Error, http.StatusInternalServerError, "Failed to delete lock %s: %s", idUnencoded, err)
		return
	}
	if lock == nil {
		s.respond(w, logging.Warn, http.StatusNotFound, "No lock found at that id", idUnencoded)
		return
	}
	s.respond(w, logging.Info, http.StatusOK, "Deleted lock id %s", idUnencoded)
}

// postEvents handles POST requests to our /events endpoint. These should be
// VCS webhook requests.
func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	s.EventsController.Post(w, r)
}

// respond is a helper function to respond and log the response. lvl is the log
// level to log at, code is the HTTP response code.
func (s *Server) respond(w http.ResponseWriter, lvl logging.LogLevel, code int, format string, args ...interface{}) {
	response := fmt.Sprintf(format, args...)
	s.Logger.Log(lvl, response)
	w.WriteHeader(code)
	fmt.Fprintln(w, response)
}
