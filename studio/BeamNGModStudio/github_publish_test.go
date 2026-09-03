package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type githubPublishTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *githubPublishTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *githubPublishTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type githubPublishHTTPReply struct {
	status  int
	headers http.Header
	body    string
	err     error
}

type githubPublishHTTPSequence struct {
	mu       sync.Mutex
	replies  []githubPublishHTTPReply
	requests []*http.Request
	bodies   []string
}

func (sequence *githubPublishHTTPSequence) Do(request *http.Request) (*http.Response, error) {
	sequence.mu.Lock()
	defer sequence.mu.Unlock()
	sequence.requests = append(sequence.requests, request)
	body, _ := io.ReadAll(request.Body)
	sequence.bodies = append(sequence.bodies, string(body))
	if len(sequence.replies) == 0 {
		return nil, errors.New("unexpected HTTP request")
	}
	reply := sequence.replies[0]
	sequence.replies = sequence.replies[1:]
	if reply.err != nil {
		return nil, reply.err
	}
	headers := reply.headers
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: reply.status, Header: headers, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
}

func TestGitHubPublishConfigurationRequiresPublicClientID(t *testing.T) {
	previous := GitHubPublishOAuthClientID
	GitHubPublishOAuthClientID = ""
	t.Cleanup(func() { GitHubPublishOAuthClientID = previous })
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{})
	configuration := service.Configuration()
	if configuration.Configured || configuration.ClientIDConfigured {
		t.Fatalf("configuration unexpectedly reports a missing client ID as configured: %#v", configuration)
	}
	if !strings.Contains(configuration.Action, "BEAMWORLDS_GITHUB_OAUTH_CLIENT_ID") {
		t.Fatalf("configuration action is not actionable: %q", configuration.Action)
	}
	if len(configuration.VisibilityOptions) != 2 || configuration.VisibilityOptions[0] != "private" || configuration.VisibilityOptions[1] != "public" {
		t.Fatalf("visibility options = %#v", configuration.VisibilityOptions)
	}
}

func TestGitHubPublishDevicePollHonorsIntervalAndVerifiesScope(t *testing.T) {
	previous := GitHubPublishOAuthClientID
	GitHubPublishOAuthClientID = "public-client-id"
	t.Cleanup(func() { GitHubPublishOAuthClientID = previous })
	clock := &githubPublishTestClock{now: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}
	sequence := &githubPublishHTTPSequence{replies: []githubPublishHTTPReply{
		{status: http.StatusOK, body: `{"device_code":"device-secret","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`},
		{status: http.StatusOK, body: `{"error":"authorization_pending"}`},
		{status: http.StatusOK, body: `{"access_token":"gho_secret","token_type":"bearer","scope":"repo"}`},
		{status: http.StatusOK, headers: http.Header{"X-OAuth-Scopes": []string{"repo"}}, body: `{"login":"beam-user"}`},
	}}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: sequence, Now: clock.Now, Sleep: func(context.Context, time.Duration) error { return nil }})
	started, err := service.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if started.State != GitHubPublishAuthDeviceWaiting || started.UserCode != "ABCD-EFGH" || started.PollIntervalSeconds != 5 {
		t.Fatalf("unexpected device state: %#v", started)
	}
	if strings.Contains(mustJSON(t, started), "device-secret") || strings.Contains(mustJSON(t, started), "gho_secret") {
		t.Fatal("device or access token was exposed in the auth model")
	}
	before := len(sequence.requests)
	tooSoon, err := service.PollDeviceAuth(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sequence.requests) != before || tooSoon.State != GitHubPublishAuthDeviceWaiting {
		t.Fatalf("early poll was not paced: requests=%d state=%s", len(sequence.requests), tooSoon.State)
	}
	clock.Advance(5 * time.Second)
	pending, err := service.PollDeviceAuth(context.Background(), started.SessionID)
	if err != nil || pending.ErrorCode != "authorization_pending" {
		t.Fatalf("pending poll = %#v, err=%v", pending, err)
	}
	clock.Advance(5 * time.Second)
	authenticated, err := service.PollDeviceAuth(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.State != GitHubPublishAuthAuthenticated || authenticated.Login != "beam-user" || !githubPublishHasScope(authenticated.GrantedScopes, "repo") {
		t.Fatalf("unexpected authenticated state: %#v", authenticated)
	}
	for _, request := range sequence.requests {
		if request.Header.Get("Authorization") != "" && request.URL.Path != "/user" {
			t.Fatal("authorization header was sent to a device endpoint")
		}
	}
}

func TestGitHubPublishReducedScopeStopsBeforePreflight(t *testing.T) {
	previous := GitHubPublishOAuthClientID
	GitHubPublishOAuthClientID = "public-client-id"
	t.Cleanup(func() { GitHubPublishOAuthClientID = previous })
	sequence := &githubPublishHTTPSequence{replies: []githubPublishHTTPReply{
		{status: http.StatusOK, body: `{"device_code":"device-secret","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`},
		{status: http.StatusOK, body: `{"access_token":"gho_secret","token_type":"bearer","scope":"public_repo"}`},
		{status: http.StatusOK, body: `{"login":"beam-user"}`},
	}}
	clock := &githubPublishTestClock{now: time.Unix(0, 0)}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: sequence, Now: clock.Now})
	started, err := service.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	state, err := service.PollDeviceAuth(context.Background(), started.SessionID)
	if err == nil || state.State != GitHubPublishAuthError || state.ErrorCode != "insufficient_permission" {
		t.Fatalf("reduced scope state = %#v, err=%v", state, err)
	}
}

func TestGitHubPublishFreshVisibilityAndRemoteSelection(t *testing.T) {
	name, description, visibility, err := githubPublishNormalizeDraft(GitHubPublishDraft{Name: "mod-maker", Description: "demo"})
	if err != nil || visibility != "private" || name != "mod-maker" || description != "demo" {
		t.Fatalf("fresh draft normalization = %q, %q, %q, %v", name, description, visibility, err)
	}
	if _, _, _, err := githubPublishNormalizeDraft(GitHubPublishDraft{Name: "bad/name", Visibility: "private"}); err == nil {
		t.Fatal("invalid repository name was accepted")
	}
	if _, _, _, err := githubPublishNormalizeDraft(GitHubPublishDraft{Name: "mod-maker", Visibility: "internal"}); err == nil {
		t.Fatal("unsupported visibility was accepted")
	}
	remotes := []githubPublishRemote{
		{name: "origin", fetchURL: "https://example.invalid/mod.git", pushURL: "https://example.invalid/mod.git"},
		{name: "github", fetchURL: "https://example.invalid/other.git", pushURL: "https://example.invalid/other.git"},
		{name: "github-2", fetchURL: "https://github.com/beam-user/mod-maker.git", pushURL: "https://github.com/beam-user/mod-maker.git"},
	}
	name, exact := githubPublishChooseRemote(remotes, "beam-user", "mod-maker")
	if name != "github-2" || exact == nil {
		t.Fatalf("remote selection = %q, %#v", name, exact)
	}
}

func TestGitHubPublishModelsNeverMarshalCredentials(t *testing.T) {
	value := GitHubPublishOperation{Partial: GitHubPublishPartialState{RepositoryURL: "https://github.com/beam-user/mod-maker", RemoteURL: "https://github.com/beam-user/mod-maker.git"}, Error: "authorization Bearer <redacted>"}
	encoded := mustJSON(t, value)
	for _, secret := range []string{"device-secret", "gho_secret", "refresh-secret", "Authorization: Bearer"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("secret-like value appeared in JSON model: %q", secret)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestGitHubPublishDeviceErrorsAreActionableAndClearCode(t *testing.T) {
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{Now: time.Now})
	session := service.newSession()
	session.state = GitHubPublishAuthDeviceWaiting
	session.deviceCode = "device-secret"
	session.userCode = "ABCD-EFGH"
	session.verificationURI = "https://github.com/login/device"
	session.interval = 5 * time.Second
	session.polling = true
	session.nextPollAt = time.Now()
	pending, err := service.handleDeviceTokenError(session, http.StatusOK, githubPublishTokenResponse{ErrorCode: "slow_down", Interval: 12})
	if err != nil || pending.PollIntervalSeconds != 12 {
		t.Fatalf("slow_down state = %#v, err=%v", pending, err)
	}
	denied, err := service.handleDeviceTokenError(session, http.StatusBadRequest, githubPublishTokenResponse{ErrorCode: "access_denied"})
	if err == nil || denied.State != GitHubPublishAuthDenied || denied.UserCode != "" || denied.VerificationURI != "" {
		t.Fatalf("access_denied state = %#v, err=%v", denied, err)
	}
}
func TestGitHubPublishPublicNeedsSecondConfirmation(t *testing.T) {
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{})
	session := service.newSession()
	session.state = GitHubPublishAuthAuthenticated
	session.login = "beam-user"
	session.accessToken = "in-memory-only"
	service.sessions[session.id] = session
	plan := &githubPublishPlan{id: "plan-public", sessionID: session.id, owner: "beam-user", name: "mod-maker", visibility: "public", targetState: "absent_or_inaccessible"}
	service.plans[plan.id] = plan
	_, err := service.Start(context.Background(), GitHubPublishStartRequest{PlanID: plan.id, SessionID: session.id, Confirmed: true})
	if err == nil || !strings.Contains(err.Error(), "second explicit Public confirmation") {
		t.Fatalf("public start without second confirmation err = %v", err)
	}
}

func TestGitHubPublishExistingPublicTargetRequiresPublicConfirmation(t *testing.T) {
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{})
	session := service.newSession()
	session.state = GitHubPublishAuthAuthenticated
	session.login = "beam-user"
	session.accessToken = "in-memory-only"
	service.sessions[session.id] = session
	target := &githubPublishRepo{ID: 42, Name: "mod-maker", FullName: "beam-user/mod-maker", OwnerLogin: "beam-user", Private: false, HTMLURL: "https://github.com/beam-user/mod-maker", CloneURL: "https://github.com/beam-user/mod-maker.git", CanPush: true}
	plan := &githubPublishPlan{id: "plan-public-existing", sessionID: session.id, owner: "beam-user", name: "mod-maker", visibility: "public", target: target, targetState: "existing_accessible"}
	service.plans[plan.id] = plan
	model := githubPublishTargetModel(target, "beam-user", "mod-maker")
	if model.Private || !model.Exists || !model.CanPush {
		t.Fatalf("public target model = %#v", model)
	}
	_, err := service.Start(context.Background(), GitHubPublishStartRequest{PlanID: plan.id, SessionID: session.id, Confirmed: true, ConnectExisting: true})
	if err == nil || !strings.Contains(err.Error(), "second explicit Public confirmation") {
		t.Fatalf("existing public target without Public confirmation err = %v", err)
	}
}

func TestGitHubPublishTargetBindingRejectsIDAndVisibilityTOCTOU(t *testing.T) {
	plan := &githubPublishPlan{target: &githubPublishRepo{ID: 7, Private: true}}
	if !githubPublishTargetBindingMatches(plan, &githubPublishRepo{ID: 7, Private: true}) {
		t.Fatal("unchanged target binding was rejected")
	}
	if githubPublishTargetBindingMatches(plan, &githubPublishRepo{ID: 8, Private: true}) {
		t.Fatal("repository ID replacement was accepted")
	}
	if githubPublishTargetBindingMatches(plan, &githubPublishRepo{ID: 7, Private: false}) {
		t.Fatal("repository visibility change was accepted")
	}
}

func TestGitHubPublishExpiredSessionIsWipedAndRemoved(t *testing.T) {
	clock := &githubPublishTestClock{now: time.Unix(100, 0)}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{Now: clock.Now})
	session := service.newSession()
	session.state = GitHubPublishAuthAuthenticated
	session.accessToken = "in-memory-only"
	session.tokenExpiresAt = clock.Now().Add(time.Second)
	service.sessions[session.id] = session
	service.plans["plan-expired"] = &githubPublishPlan{sessionID: session.id}
	clock.Advance(2 * time.Second)
	if !service.expireSessionIfNeeded(session) {
		t.Fatal("expired session was not recognized")
	}
	if session.accessToken != "" || session.tokenExpiresAt != (time.Time{}) {
		t.Fatal("expired session token was not wiped")
	}
	if _, ok := service.sessions[session.id]; ok {
		t.Fatal("expired session remained in service map")
	}
	if _, ok := service.plans["plan-expired"]; ok {
		t.Fatal("expired session plan remained in service map")
	}
}

func TestGitHubPublishRejectsNestedRepositoryBeforeHTTP(t *testing.T) {
	parentRoot := t.TempDir()
	nestedRoot := filepath.Join(parentRoot, "workspace")
	if err := os.Mkdir(nestedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(nestedRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	httpSequence := &githubPublishHTTPSequence{}
	session := &githubPublishSession{id: "nested-session", state: GitHubPublishAuthAuthenticated, login: "beam-user", accessToken: "in-memory-only", ctx: context.Background(), cancel: func() {}, operationIDs: make(map[string]struct{})}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: httpSequence, Git: NewGitServiceWithRunner(&githubPublishFixtureGitRunner{root: nestedRoot, showTop: parentRoot})})
	service.sessions[session.id] = session
	_, err := service.Preflight(context.Background(), nestedRoot, GitHubPublishDraft{SessionID: session.id, Name: "mod-maker"})
	if err == nil || !strings.Contains(err.Error(), "does not exactly match") {
		t.Fatalf("nested repository error = %v", err)
	}
	if len(httpSequence.requests) != 0 {
		t.Fatalf("nested repository made provider requests: %d", len(httpSequence.requests))
	}
}

func TestGitHubPublishPollCancellationCannotCommitToken(t *testing.T) {
	previous := GitHubPublishOAuthClientID
	GitHubPublishOAuthClientID = "public-client-id"
	t.Cleanup(func() { GitHubPublishOAuthClientID = previous })
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "shutdown"}[shutdown], func(t *testing.T) {
			clock := &githubPublishTestClock{now: time.Unix(200, 0)}
			httpClient := newGitHubPublishAuthRaceHTTP()
			service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: httpClient, Now: clock.Now})
			started, err := service.StartDeviceAuth(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(time.Second)
			resultChannel := make(chan struct {
				model GitHubPublishAuthSession
				err   error
			}, 1)
			go func() {
				model, pollErr := service.PollDeviceAuth(context.Background(), started.SessionID)
				resultChannel <- struct {
					model GitHubPublishAuthSession
					err   error
				}{model: model, err: pollErr}
			}()
			<-httpClient.userStarted
			if shutdown {
				service.Shutdown()
			} else if _, err := service.CancelAuth(started.SessionID); err != nil {
				t.Fatal(err)
			}
			close(httpClient.release)
			result := <-resultChannel
			if result.model.State == GitHubPublishAuthAuthenticated || result.err == nil {
				t.Fatalf("cancelled poll committed authentication: %#v", result)
			}
			session, ok := service.sessions[started.SessionID]
			if ok {
				session.mu.Lock()
				token := session.accessToken
				state := session.state
				session.mu.Unlock()
				if token != "" || state == GitHubPublishAuthAuthenticated {
					t.Fatalf("cancelled session retained authentication: state=%s token=%q", state, token)
				}
			}
		})
	}
}

func TestGitHubPublishClearSessionCannotStorePreflightPlan(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	httpClient := newGitHubPublishPreflightBlockHTTP()
	sessionContext, sessionCancel := context.WithCancel(context.Background())
	session := &githubPublishSession{id: "preflight-session", state: GitHubPublishAuthAuthenticated, login: "beam-user", accessToken: "in-memory-only", ctx: sessionContext, cancel: sessionCancel, operationIDs: make(map[string]struct{})}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: httpClient, Git: NewGitServiceWithRunner(&githubPublishFixtureGitRunner{root: root, showTop: root})})
	service.sessions[session.id] = session
	resultChannel := make(chan struct {
		model GitHubPublishPreflight
		err   error
	}, 1)
	go func() {
		model, preflightErr := service.Preflight(context.Background(), root, GitHubPublishDraft{SessionID: session.id, Name: "mod-maker"})
		resultChannel <- struct {
			model GitHubPublishPreflight
			err   error
		}{model: model, err: preflightErr}
	}()
	<-httpClient.started
	if err := service.ClearSession(session.id); err != nil {
		t.Fatal(err)
	}
	close(httpClient.release)
	result := <-resultChannel
	if result.err == nil || result.model.PlanID != "" {
		t.Fatalf("preflight stored a plan after clear: %#v", result)
	}
	if len(service.plans) != 0 {
		t.Fatalf("stale preflight plan count = %d", len(service.plans))
	}
}

func TestGitHubPublishRemoteAddCancellationIsUnknown(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &githubPublishFixtureGitRunner{root: root, showTop: root, cancelRemoteAdd: true}
	httpSequence := &githubPublishHTTPSequence{replies: []githubPublishHTTPReply{
		{status: http.StatusNotFound},
		{status: http.StatusCreated, body: `{"id":99,"name":"mod-maker","full_name":"beam-user/mod-maker","owner":{"login":"beam-user"},"description":"","private":true,"html_url":"https://github.com/beam-user/mod-maker","clone_url":"https://github.com/beam-user/mod-maker.git","permissions":{"push":true}}`},
	}}
	session := &githubPublishSession{id: "remote-session", state: GitHubPublishAuthAuthenticated, login: "beam-user", accessToken: "in-memory-only", ctx: context.Background(), cancel: func() {}, operationIDs: make(map[string]struct{})}
	service := NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{HTTP: httpSequence, Git: NewGitServiceWithRunner(runner), Sleep: func(context.Context, time.Duration) error { return nil }})
	service.sessions[session.id] = session
	plan := &githubPublishPlan{id: "remote-plan", sessionID: session.id, root: root, owner: "beam-user", name: "mod-maker", visibility: "private", localOID: strings.Repeat("a", 40), targetState: "absent_or_inaccessible"}
	service.plans[plan.id] = plan
	operationContext, cancel := context.WithCancel(context.Background())
	runner.cancel = cancel
	operation := &githubPublishOperation{cancel: cancel, done: make(chan struct{}), plan: plan, model: GitHubPublishOperation{OperationID: "remote-operation", SessionID: session.id, Steps: githubPublishInitialSteps(), Partial: GitHubPublishPartialState{RemoteState: "not_started", PushState: "not_started"}}}
	service.runOperation(operation, session, GitHubPublishStartRequest{PlanID: plan.id, SessionID: session.id, Confirmed: true}, operationContext)
	if operation.model.State != GitHubPublishOperationCancelled || operation.model.Partial.RemoteState != "unknown" || !operation.model.ReconciliationRequired {
		t.Fatalf("ambiguous remote-add state = %#v", operation.model)
	}
}

type githubPublishFixtureGitRunner struct {
	mu              sync.Mutex
	root            string
	showTop         string
	remoteAdded     bool
	cancelRemoteAdd bool
	cancel          func()
}

func (runner *githubPublishFixtureGitRunner) Run(_ context.Context, _ string, _ string, args ...string) (string, string, error) {
	key := strings.Join(args, " ")
	switch key {
	case "rev-parse --show-toplevel":
		return runner.showTop + "\n", "", nil
	case "rev-parse --is-inside-work-tree":
		return "true\n", "", nil
	case "rev-parse --git-dir":
		return ".git\n", "", nil
	case "symbolic-ref --quiet --short HEAD":
		return "main\n", "", nil
	case "show-ref --verify --quiet refs/heads/main":
		return "", "", nil
	case "rev-parse --verify refs/heads/main^{commit}":
		return strings.Repeat("a", 40) + "\n", "", nil
	case "remote -v":
		runner.mu.Lock()
		added := runner.remoteAdded
		runner.mu.Unlock()
		if added {
			return "origin https://github.com/beam-user/mod-maker.git (fetch)\norigin https://github.com/beam-user/mod-maker.git (push)\n", "", nil
		}
		return "", "", nil
	}
	if len(args) >= 3 && args[0] == "remote" && args[1] == "add" {
		runner.mu.Lock()
		runner.remoteAdded = true
		cancel := runner.cancel
		cancelRemoteAdd := runner.cancelRemoteAdd
		runner.mu.Unlock()
		if cancelRemoteAdd && cancel != nil {
			cancel()
		}
		return "", "", context.Canceled
	}
	return "", "unexpected fixture Git command: " + key, errors.New("unexpected fixture Git command")
}

type githubPublishAuthRaceHTTP struct {
	userStarted chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func newGitHubPublishAuthRaceHTTP() *githubPublishAuthRaceHTTP {
	return &githubPublishAuthRaceHTTP{userStarted: make(chan struct{}), release: make(chan struct{})}
}

func (client *githubPublishAuthRaceHTTP) Do(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/login/device/code":
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"device_code":"device-secret","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`))}, nil
	case "/login/oauth/access_token":
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"gho_secret","token_type":"bearer","scope":"repo","expires_in":600}`))}, nil
	case "/user":
		client.startedOnce.Do(func() { close(client.userStarted) })
		select {
		case <-client.release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-OAuth-Scopes": []string{"repo"}}, Body: io.NopCloser(strings.NewReader(`{"login":"beam-user"}`))}, nil
	default:
		return nil, errors.New("unexpected auth race HTTP request")
	}
}

type githubPublishPreflightBlockHTTP struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
}

func newGitHubPublishPreflightBlockHTTP() *githubPublishPreflightBlockHTTP {
	return &githubPublishPreflightBlockHTTP{started: make(chan struct{}), release: make(chan struct{})}
}

func (client *githubPublishPreflightBlockHTTP) Do(_ *http.Request) (*http.Response, error) {
	client.startedOnce.Do(func() { close(client.started) })
	<-client.release
	return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}
