package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// GitHubPublishOAuthClientID is the public OAuth App identifier used by the
// device authorization flow. It is intentionally not a secret. A release may
// set it at link time; BEAMWORLDS_GITHUB_OAUTH_CLIENT_ID is used when this
// variable is empty.
var GitHubPublishOAuthClientID string

const (
	githubPublishDeviceCodeEndpoint = "https://github.com/login/device/code"
	githubPublishTokenEndpoint      = "https://github.com/login/oauth/access_token"
	githubPublishAPIEndpoint        = "https://api.github.com"
	githubPublishAPIVersion         = "2026-03-10"
	githubPublishScope              = "repo"

	githubPublishMaxBodyBytes                 = 4 << 20
	githubPublishMaxCreationReconcileAttempts = 3
	githubPublishMaxErrorLength               = 512
)

const githubPublishMaxSessionLifetime = 15 * time.Minute

const (
	GitHubPublishAuthIdle                   = "idle"
	GitHubPublishAuthStarting               = "auth_starting"
	GitHubPublishAuthDeviceWaiting          = "device_waiting"
	GitHubPublishAuthAuthenticated          = "authenticated"
	GitHubPublishAuthInsufficientPermission = "insufficient_permission"
	GitHubPublishAuthExpired                = "expired"
	GitHubPublishAuthDenied                 = "denied"
	GitHubPublishAuthCancelled              = "cancelled"
	GitHubPublishAuthError                  = "error"
	GitHubPublishAuthSessionEnded           = "session_ended"
)

const (
	GitHubPublishOperationQueued       = "queued"
	GitHubPublishOperationRunning      = "running"
	GitHubPublishOperationComplete     = "complete"
	GitHubPublishOperationFailed       = "failed"
	GitHubPublishOperationCancelled    = "cancelled"
	GitHubPublishOperationNeedsConfirm = "needs_confirmation"
	GitHubPublishOperationUnknown      = "unknown"
)

const (
	GitHubPublishStepPending   = "pending"
	GitHubPublishStepRunning   = "running"
	GitHubPublishStepSucceeded = "succeeded"
	GitHubPublishStepFailed    = "failed"
	GitHubPublishStepSkipped   = "skipped"
	GitHubPublishStepUnknown   = "unknown"
	GitHubPublishStepCancelled = "cancelled"
)

// GitHubPublishHTTPDoer is the injectable HTTP boundary used by the publisher.
// http.Client implements it. Tests can provide an offline deterministic client.
type GitHubPublishHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type GitHubPublishSleepFunc func(context.Context, time.Duration) error
type GitHubPublishNowFunc func() time.Time

// GitHubPublishDependencies keeps network, time, and Git boundaries explicit.
// The public constructor is useful to deterministic tests without changing
// production authentication or Git behavior.
type GitHubPublishDependencies struct {
	HTTP          GitHubPublishHTTPDoer
	Git           *GitService
	Now           GitHubPublishNowFunc
	Sleep         GitHubPublishSleepFunc
	DeviceURL     string
	TokenURL      string
	APIBaseURL    string
	WorkspaceLock func(string) *sync.Mutex
}

// GitHubPublishConfiguration describes only non-secret integration setup.
type GitHubPublishConfiguration struct {
	Provider           string   `json:"provider"`
	Configured         bool     `json:"configured"`
	ClientIDConfigured bool     `json:"clientIdConfigured"`
	Scope              string   `json:"scope"`
	VisibilityOptions  []string `json:"visibilityOptions"`
	Message            string   `json:"message"`
	Action             string   `json:"action"`
}

// GitHubPublishAuthSession is the device-flow state exposed to the UI. Device
// and access tokens are deliberately absent; only the user code intended for
// display and the verified account are returned.
type GitHubPublishAuthSession struct {
	SessionID           string   `json:"sessionId"`
	Configured          bool     `json:"configured"`
	State               string   `json:"state"`
	Login               string   `json:"login"`
	UserCode            string   `json:"userCode"`
	VerificationURI     string   `json:"verificationUri"`
	ExpiresAt           string   `json:"expiresAt"`
	ExpiresInSeconds    int      `json:"expiresInSeconds"`
	PollIntervalSeconds int      `json:"pollIntervalSeconds"`
	NextPollAt          string   `json:"nextPollAt"`
	GrantedScopes       []string `json:"grantedScopes"`
	ErrorCode           string   `json:"errorCode"`
	Error               string   `json:"error"`
	Action              string   `json:"action"`
	Retryable           bool     `json:"retryable"`
}

// GitHubPublishDraft is a fresh repository draft. Visibility is normalized to
// exactly "private" or "public" by the backend; an empty value means private.
type GitHubPublishDraft struct {
	SessionID       string `json:"sessionId"`
	WorkspaceID     string `json:"workspaceId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Visibility      string `json:"visibility"`
	PublicConfirmed bool   `json:"publicConfirmed"`
}

// GitHubPublishRemote is a secret-free view of one local remote.
type GitHubPublishRemote struct {
	Name          string `json:"name"`
	FetchURL      string `json:"fetchUrl"`
	PushURL       string `json:"pushUrl"`
	MatchesTarget bool   `json:"matchesTarget"`
}

// GitHubPublishLocalState captures the exact local state used for the
// preflight and reconciliation decision.
type GitHubPublishLocalState struct {
	Root          string                `json:"root"`
	GitDirectory  string                `json:"gitDirectory"`
	Repository    bool                  `json:"repository"`
	CurrentBranch string                `json:"currentBranch"`
	Detached      bool                  `json:"detached"`
	MainAvailable bool                  `json:"mainAvailable"`
	MainOID       string                `json:"mainOid"`
	Remotes       []GitHubPublishRemote `json:"remotes"`
}

// GitHubPublishTarget distinguishes an accessible collision from GitHub's
// deliberately ambiguous not-found response.
type GitHubPublishTarget struct {
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	CloneURL     string `json:"cloneUrl"`
	RepositoryID int64  `json:"repositoryId"`
	State        string `json:"state"`
	Exists       bool   `json:"exists"`
	AccessKnown  bool   `json:"accessKnown"`
	Accessible   bool   `json:"accessible"`
	CanPush      bool   `json:"canPush"`
	Private      bool   `json:"private"`
	Description  string `json:"description"`
	Error        string `json:"error"`
	Action       string `json:"action"`
}

// GitHubPublishPreflight is a read-only plan. Nothing in this call creates a
// repository or changes the local Git configuration.
type GitHubPublishPreflight struct {
	PlanID                      string                  `json:"planId"`
	SessionID                   string                  `json:"sessionId"`
	Ready                       bool                    `json:"ready"`
	Visibility                  string                  `json:"visibility"`
	VisibilityOptions           []string                `json:"visibilityOptions"`
	Owner                       string                  `json:"owner"`
	Name                        string                  `json:"name"`
	Description                 string                  `json:"description"`
	TargetURL                   string                  `json:"targetUrl"`
	Local                       GitHubPublishLocalState `json:"local"`
	Target                      GitHubPublishTarget     `json:"target"`
	ProposedRemoteName          string                  `json:"proposedRemoteName"`
	ProposedRemoteURL           string                  `json:"proposedRemoteUrl"`
	ExistingOrigin              *GitHubPublishRemote    `json:"existingOrigin,omitempty"`
	RequiresFinalConfirmation   bool                    `json:"requiresFinalConfirmation"`
	RequiresPublicConfirmation  bool                    `json:"requiresPublicConfirmation"`
	RequiresConnectConfirmation bool                    `json:"requiresConnectConfirmation"`
	CreationUnknown             bool                    `json:"creationUnknown"`
	ReconciliationRequired      bool                    `json:"reconciliationRequired"`
	Retryable                   bool                    `json:"retryable"`
	Error                       string                  `json:"error"`
	Action                      string                  `json:"action"`
}

// GitHubPublishStartRequest is the final, explicit confirmation boundary.
type GitHubPublishStartRequest struct {
	PlanID               string `json:"planId"`
	SessionID            string `json:"sessionId"`
	Confirmed            bool   `json:"confirmed"`
	PublicConfirmed      bool   `json:"publicConfirmed"`
	ConnectExisting      bool   `json:"connectExisting"`
	RetryUnknownCreation bool   `json:"retryUnknownCreation"`
}

// GitHubPublishStep is one ordered state-machine step.
type GitHubPublishStep struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
}

// GitHubPublishPartialState remains truthful after cancellation or a partial
// transfer. It intentionally contains no credential or provider token.
type GitHubPublishPartialState struct {
	CreatedURL       string `json:"createdUrl"`
	RepositoryURL    string `json:"repositoryUrl"`
	RemoteName       string `json:"remoteName"`
	RemoteURL        string `json:"remoteUrl"`
	RemoteState      string `json:"remoteState"`
	RemoteAdded      bool   `json:"remoteAdded"`
	RemoteReused     bool   `json:"remoteReused"`
	PushState        string `json:"pushState"`
	UpstreamVerified bool   `json:"upstreamVerified"`
	UpstreamRemote   string `json:"upstreamRemote"`
	UpstreamMerge    string `json:"upstreamMerge"`
}

// GitHubPublishOperation is the asynchronously polled operation/result.
type GitHubPublishOperation struct {
	OperationID            string                    `json:"operationId"`
	SessionID              string                    `json:"sessionId"`
	State                  string                    `json:"state"`
	Steps                  []GitHubPublishStep       `json:"steps"`
	Partial                GitHubPublishPartialState `json:"partial"`
	Error                  string                    `json:"error"`
	Action                 string                    `json:"action"`
	Retryable              bool                      `json:"retryable"`
	ReconciliationRequired bool                      `json:"reconciliationRequired"`
	Cancelled              bool                      `json:"cancelled"`
	Completed              bool                      `json:"completed"`
}

type githubPublishSession struct {
	mu               sync.Mutex
	id               string
	ctx              context.Context
	cancel           context.CancelFunc
	state            string
	login            string
	userCode         string
	verificationURI  string
	expiresAt        time.Time
	expiresInSeconds int
	tokenExpiresAt   time.Time
	interval         time.Duration
	nextPollAt       time.Time
	pollFailures     int
	polling          bool
	pollGeneration   uint64
	grantedScopes    []string
	errorCode        string
	error            string
	action           string
	retryable        bool
	accessToken      string
	deviceCode       string
	operationIDs     map[string]struct{}
}

type githubPublishRemote struct {
	name     string
	fetchURL string
	pushURL  string
}

type githubPublishLocalSnapshot struct {
	model        GitHubPublishLocalState
	remotes      []githubPublishRemote
	mainOID      string
	gitDirectory string
}

type githubPublishRepo struct {
	ID          int64
	Name        string
	FullName    string
	OwnerLogin  string
	Description string
	Private     bool
	HTMLURL     string
	CloneURL    string
	CanPush     bool
}

type githubPublishPlan struct {
	id              string
	sessionID       string
	workspaceID     string
	root            string
	owner           string
	name            string
	description     string
	visibility      string
	localOID        string
	local           githubPublishLocalSnapshot
	target          *githubPublishRepo
	targetState     string
	creationUnknown bool
	createdAt       time.Time
}

type githubPublishOperation struct {
	mu      sync.Mutex
	model   GitHubPublishOperation
	cancel  context.CancelFunc
	release func()
	plan    *githubPublishPlan
	done    chan struct{}
}

type githubPublishService struct {
	rootCtx context.Context
	cancel  context.CancelFunc

	mu               sync.Mutex
	closed           bool
	sessions         map[string]*githubPublishSession
	plans            map[string]*githubPublishPlan
	operations       map[string]*githubPublishOperation
	uncertainCreates map[string]struct{}

	http          GitHubPublishHTTPDoer
	git           *GitService
	now           GitHubPublishNowFunc
	sleep         GitHubPublishSleepFunc
	deviceURL     string
	tokenURL      string
	apiBaseURL    string
	workspaceLock func(string) *sync.Mutex
}

// GitHubPublishService is the production backend. AppService owns one for
// the lifetime of the application.
type GitHubPublishService = githubPublishService

func NewGitHubPublishService(git *GitService) *GitHubPublishService {
	return NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{Git: git})
}

func NewGitHubPublishServiceWithDependencies(deps GitHubPublishDependencies) *GitHubPublishService {
	rootCtx, cancel := context.WithCancel(context.Background())
	git := deps.Git
	if git == nil {
		git = NewGitService()
	}
	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	sleep := deps.Sleep
	if sleep == nil {
		sleep = githubPublishSleep
	}
	deviceURL := strings.TrimRight(strings.TrimSpace(deps.DeviceURL), "/")
	if deviceURL == "" {
		deviceURL = githubPublishDeviceCodeEndpoint
	}
	tokenURL := strings.TrimRight(strings.TrimSpace(deps.TokenURL), "/")
	if tokenURL == "" {
		tokenURL = githubPublishTokenEndpoint
	}
	apiURL := strings.TrimRight(strings.TrimSpace(deps.APIBaseURL), "/")
	if apiURL == "" {
		apiURL = githubPublishAPIEndpoint
	}
	return &githubPublishService{
		rootCtx:          rootCtx,
		cancel:           cancel,
		sessions:         make(map[string]*githubPublishSession),
		plans:            make(map[string]*githubPublishPlan),
		operations:       make(map[string]*githubPublishOperation),
		uncertainCreates: make(map[string]struct{}),
		http:             httpClient,
		git:              git,
		now:              now,
		sleep:            sleep,
		deviceURL:        deviceURL,
		tokenURL:         tokenURL,
		apiBaseURL:       apiURL,
		workspaceLock:    deps.WorkspaceLock,
	}
}

func githubPublishSleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func githubPublishClientID() string {
	if configured := strings.TrimSpace(GitHubPublishOAuthClientID); configured != "" {
		return configured
	}
	return strings.TrimSpace(os.Getenv("BEAMWORLDS_GITHUB_OAUTH_CLIENT_ID"))
}

func (service *githubPublishService) Configuration() GitHubPublishConfiguration {
	configured := githubPublishClientID() != ""
	message := "GitHub device authorization is ready."
	action := ""
	if !configured {
		message = "GitHub publishing is not configured because no public OAuth client ID is available."
		action = "Register a GitHub OAuth App with Device Flow enabled and set BEAMWORLDS_GITHUB_OAUTH_CLIENT_ID, or provide the public link-time client ID."
	}
	return GitHubPublishConfiguration{
		Provider:           "github",
		Configured:         configured,
		ClientIDConfigured: configured,
		Scope:              githubPublishScope,
		VisibilityOptions:  []string{"private", "public"},
		Message:            message,
		Action:             action,
	}
}

func (service *githubPublishService) newSession() *githubPublishSession {
	ctx, cancel := context.WithCancel(service.rootCtx)
	return &githubPublishSession{
		id:           uuid.NewString(),
		ctx:          ctx,
		cancel:       cancel,
		state:        GitHubPublishAuthIdle,
		operationIDs: make(map[string]struct{}),
	}
}

func (service *githubPublishService) StartDeviceAuth(ctx context.Context) (GitHubPublishAuthSession, error) {
	session := service.newSession()
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		return GitHubPublishAuthSession{SessionID: session.id, Configured: false, State: GitHubPublishAuthError, ErrorCode: "service_closed", Error: "GitHub publishing is shutting down.", Action: "Restart Mod Studio and try again."}, errors.New("GitHub publishing service is closed")
	}
	service.sessions[session.id] = session
	service.mu.Unlock()

	clientID := githubPublishClientID()
	configured := clientID != ""
	session.mu.Lock()
	session.state = GitHubPublishAuthStarting
	session.mu.Unlock()
	if !configured {
		service.setSessionError(session, "not_configured", "GitHub publishing is not configured.", "Set BEAMWORLDS_GITHUB_OAUTH_CLIENT_ID to a registered public OAuth client ID and enable Device Flow.", false)
		model := service.sessionSnapshot(session)
		model.Configured = false
		return model, errors.New(model.Error)
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", githubPublishScope)
	requestCtx, release := githubPublishLinkedContext(ctx, session.ctx)
	defer release()
	status, _, body, err := service.doHTTP(requestCtx, http.MethodPost, service.deviceURL, []byte(form.Encode()), "", false)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled before device authorization started.", "Start a fresh GitHub sign-in if you still want to publish.")
			return service.sessionSnapshot(session), err
		}
		service.setSessionError(session, "device_request_failed", "GitHub device authorization could not be started.", "Check the network and GitHub OAuth App configuration, then retry.", true)
		return service.sessionSnapshot(session), err
	}
	if status != http.StatusOK {
		providerErr := githubPublishHTTPFailure(status, body)
		code := githubPublishErrorCode(providerErr)
		if code == "" {
			code = "device_request_failed"
		}
		service.setSessionError(session, code, githubPublishSafeError(providerErr), "Check the network and OAuth App settings, then retry.", status >= 500 || status == http.StatusTooManyRequests)
		return service.sessionSnapshot(session), providerErr
	}
	device, parseErr := parseGitHubDeviceResponse(body)
	if parseErr != nil || device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		if parseErr == nil {
			parseErr = errors.New("GitHub device authorization response was incomplete")
		}
		service.setSessionError(session, "invalid_device_response", "GitHub returned an incomplete device authorization response.", "Retry the sign-in; if it continues, verify Device Flow is enabled for the OAuth App.", true)
		return service.sessionSnapshot(session), parseErr
	}
	if !githubPublishVerificationURLAllowed(device.VerificationURI) {
		err = errors.New("GitHub returned an untrusted verification URL")
		service.setSessionError(session, "invalid_verification_uri", "GitHub returned an invalid verification URL.", "Retry the sign-in and contact the application administrator if it persists.", false)
		return service.sessionSnapshot(session), err
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	intervalSeconds := device.Interval
	if intervalSeconds <= 0 {
		intervalSeconds = 5
	}
	now := service.now()
	session.mu.Lock()
	if session.state != GitHubPublishAuthStarting || session.ctx.Err() != nil || service.rootCtx.Err() != nil {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		if model.Error == "" {
			return model, context.Canceled
		}
		return model, errors.New(model.Error)
	}
	session.pollGeneration++
	session.state = GitHubPublishAuthDeviceWaiting
	session.userCode = device.UserCode
	session.verificationURI = device.VerificationURI
	session.expiresInSeconds = expiresIn
	session.expiresAt = now.Add(time.Duration(expiresIn) * time.Second)
	session.interval = time.Duration(intervalSeconds) * time.Second
	session.nextPollAt = now.Add(session.interval)
	session.pollFailures = 0
	session.errorCode = ""
	session.error = ""
	session.action = ""
	session.retryable = true
	session.accessToken = ""
	session.tokenExpiresAt = time.Time{}
	session.deviceCode = device.DeviceCode
	session.mu.Unlock()
	return service.sessionSnapshot(session), nil
}

func (session *githubPublishSession) clearTransient() {
	session.userCode = ""
	session.verificationURI = ""
	session.expiresAt = time.Time{}
	session.expiresInSeconds = 0
	session.interval = 0
	session.nextPollAt = time.Time{}
	session.deviceCode = ""
}

func (session *githubPublishSession) wipeTokens() {
	session.accessToken = ""
	session.tokenExpiresAt = time.Time{}
}

func (service *githubPublishService) PollDeviceAuth(ctx context.Context, sessionID string) (GitHubPublishAuthSession, error) {
	session, err := service.lookupSession(sessionID)
	if err != nil {
		return GitHubPublishAuthSession{SessionID: strings.TrimSpace(sessionID), State: GitHubPublishAuthError, ErrorCode: "unknown_session", Error: "GitHub authentication session was not found.", Action: "Start a new GitHub sign-in."}, err
	}
	now := service.now()
	session.mu.Lock()
	if session.state == GitHubPublishAuthAuthenticated {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		return model, nil
	}
	if session.state != GitHubPublishAuthDeviceWaiting {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		if model.Error == "" {
			return model, errors.New("GitHub authentication is not waiting for a device authorization poll")
		}
		return model, errors.New(model.Error)
	}
	if !session.expiresAt.IsZero() && !now.Before(session.expiresAt) {
		session.state = GitHubPublishAuthExpired
		session.errorCode = "expired_token"
		session.error = "The GitHub device code expired before authorization completed."
		session.action = "Start a fresh GitHub device authorization."
		session.retryable = true
		session.clearTransient()
		session.wipeTokens()
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		service.removeSessionAndPlans(session)
		return model, errors.New(model.Error)
	}
	if session.polling {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		return model, nil
	}
	if !session.nextPollAt.IsZero() && now.Before(session.nextPollAt) {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		return model, nil
	}
	deviceCode := session.deviceCode
	pollGeneration := session.pollGeneration
	session.polling = true
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.polling = false
		session.mu.Unlock()
	}()

	form := url.Values{}
	form.Set("client_id", githubPublishClientID())
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	requestCtx, release := githubPublishLinkedContext(ctx, session.ctx)
	defer release()
	status, _, body, requestErr := service.doHTTP(requestCtx, http.MethodPost, service.tokenURL, []byte(form.Encode()), "", false)
	if requestErr != nil {
		if errors.Is(requestErr, context.Canceled) {
			service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled.", "Start a fresh GitHub sign-in if you still want to publish.")
			return service.sessionSnapshot(session), requestErr
		}
		session.mu.Lock()
		if session.state != GitHubPublishAuthDeviceWaiting || session.pollGeneration != pollGeneration || session.ctx.Err() != nil || service.rootCtx.Err() != nil {
			model := service.sessionSnapshotLocked(session)
			session.mu.Unlock()
			return model, requestErr
		}
		session.pollFailures++
		if session.interval <= 0 {
			session.interval = 5 * time.Second
		}
		if session.interval < 60*time.Second {
			session.interval *= 2
			if session.interval > 60*time.Second {
				session.interval = 60 * time.Second
			}
		}
		session.nextPollAt = service.now().Add(session.interval)
		session.errorCode = "network_error"
		session.error = "The GitHub authorization request could not be reached."
		session.action = "Wait for the displayed interval and try polling again; check the network if it persists."
		session.retryable = true
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		return model, requestErr
	}

	tokenResponse, parseErr := parseGitHubTokenResponse(body)
	if parseErr != nil {
		model, applied := service.setPollSessionError(session, pollGeneration, "invalid_token_response", "GitHub returned an invalid authorization response.", "Retry the device authorization from the beginning.", true)
		if !applied {
			return model, errors.New(model.Error)
		}
		return model, parseErr
	}
	if tokenResponse.ErrorCode != "" {
		return service.handleDeviceTokenError(session, status, tokenResponse)
	}
	if status != http.StatusOK || tokenResponse.AccessToken == "" || !strings.EqualFold(tokenResponse.TokenType, "bearer") {
		model, applied := service.setPollSessionError(session, pollGeneration, "invalid_token_response", "GitHub did not return a usable authorization token.", "Start a fresh device authorization.", true)
		if !applied {
			return model, errors.New(model.Error)
		}
		return model, errors.New("GitHub returned an invalid authorization response")
	}
	session.mu.Lock()
	pollLive := session.state == GitHubPublishAuthDeviceWaiting && session.polling && session.pollGeneration == pollGeneration && session.ctx.Err() == nil && service.rootCtx.Err() == nil
	session.mu.Unlock()
	if !pollLive {
		model := service.sessionSnapshot(session)
		if model.State == GitHubPublishAuthDeviceWaiting {
			service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled.", "Start a fresh GitHub sign-in.")
			model = service.sessionSnapshot(session)
		}
		return model, context.Canceled
	}
	login, scopes, verifyErr := service.verifyGitHubAccount(requestCtx, tokenResponse.AccessToken, tokenResponse.Scope)
	if verifyErr != nil {
		if errors.Is(verifyErr, context.Canceled) {
			service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled.", "Start a fresh GitHub sign-in if you still want to publish.")
			return service.sessionSnapshot(session), verifyErr
		}
		code := githubPublishErrorCode(verifyErr)
		if code == "" {
			code = "account_verification_failed"
		}
		model, applied := service.setPollSessionError(session, pollGeneration, code, githubPublishSafeError(verifyErr), "Reauthenticate with a GitHub account that can create and push repositories, then retry.", true)
		if !applied {
			return model, errors.New(model.Error)
		}
		return model, verifyErr
	}
	expiresIn := tokenResponse.ExpiresIn
	maxSeconds := int(githubPublishMaxSessionLifetime / time.Second)
	if expiresIn <= 0 || expiresIn > maxSeconds {
		expiresIn = maxSeconds
	}
	tokenExpiresAt := service.now().Add(time.Duration(expiresIn) * time.Second)
	session.mu.Lock()
	if session.state != GitHubPublishAuthDeviceWaiting || !session.polling || session.pollGeneration != pollGeneration || session.ctx.Err() != nil || service.rootCtx.Err() != nil || !service.now().Before(tokenExpiresAt) {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		if model.State == GitHubPublishAuthDeviceWaiting {
			service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled.", "Start a fresh GitHub sign-in.")
			model = service.sessionSnapshot(session)
		}
		tokenResponse.AccessToken = ""
		return model, context.Canceled
	}
	session.state = GitHubPublishAuthAuthenticated
	session.login = login
	session.grantedScopes = append([]string(nil), scopes...)
	session.accessToken = tokenResponse.AccessToken
	session.tokenExpiresAt = tokenExpiresAt
	session.deviceCode = ""
	session.userCode = ""
	session.verificationURI = ""
	session.expiresAt = time.Time{}
	session.expiresInSeconds = 0
	session.interval = 0
	session.nextPollAt = time.Time{}
	session.errorCode = ""
	session.error = ""
	session.action = ""
	session.retryable = false
	model := service.sessionSnapshotLocked(session)
	session.mu.Unlock()
	return model, nil
}

func (service *githubPublishService) setPollSessionError(session *githubPublishSession, generation uint64, code, message, action string, retryable bool) (GitHubPublishAuthSession, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != GitHubPublishAuthDeviceWaiting || !session.polling || session.pollGeneration != generation || session.ctx.Err() != nil || service.rootCtx.Err() != nil {
		return service.sessionSnapshotLocked(session), false
	}
	session.state = GitHubPublishAuthError
	session.errorCode = code
	session.error = githubPublishSafeText(message)
	session.action = githubPublishSafeText(action)
	session.retryable = retryable
	session.clearTransient()
	session.wipeTokens()
	return service.sessionSnapshotLocked(session), true
}

func (service *githubPublishService) handleDeviceTokenError(session *githubPublishSession, status int, response githubPublishTokenResponse) (GitHubPublishAuthSession, error) {
	code := strings.TrimSpace(response.ErrorCode)
	description := githubPublishSafeText(response.ErrorDescription)
	if description == "" {
		description = "GitHub did not complete device authorization."
	}
	session.mu.Lock()
	if session.state != GitHubPublishAuthDeviceWaiting || !session.polling || session.ctx.Err() != nil || service.rootCtx.Err() != nil {
		model := service.sessionSnapshotLocked(session)
		session.mu.Unlock()
		if model.Error == "" {
			return model, context.Canceled
		}
		return model, errors.New(model.Error)
	}
	switch code {
	case "authorization_pending":
		if session.interval <= 0 {
			session.interval = 5 * time.Second
		}
		session.nextPollAt = service.now().Add(session.interval)
		session.state = GitHubPublishAuthDeviceWaiting
		session.errorCode = code
		session.error = "GitHub is still waiting for authorization."
		session.action = "Complete authorization in the browser, then poll again after the displayed interval."
		session.retryable = true
	case "slow_down":
		if response.Interval > 0 {
			session.interval = time.Duration(response.Interval) * time.Second
			if session.interval < 5*time.Second {
				session.interval = 5 * time.Second
			}
		} else {
			if session.interval <= 0 {
				session.interval = 5 * time.Second
			}
			session.interval += 5 * time.Second
		}
		session.nextPollAt = service.now().Add(session.interval)
		session.state = GitHubPublishAuthDeviceWaiting
		session.errorCode = code
		session.error = "GitHub asked the application to slow authorization polling."
		session.action = "Wait for the longer displayed interval before polling again."
		session.retryable = true
	case "expired_token":
		session.state = GitHubPublishAuthExpired
		session.errorCode = code
		session.error = "The GitHub device code expired before authorization completed."
		session.action = "Start a fresh GitHub device authorization."
		session.retryable = true
		session.clearTransient()
		session.wipeTokens()
	case "access_denied":
		session.state = GitHubPublishAuthDenied
		session.errorCode = code
		session.error = "GitHub authorization was denied or cancelled."
		session.action = "Start a fresh sign-in and approve the requested repo permission if you still want to publish."
		session.retryable = true
		session.clearTransient()
		session.wipeTokens()
	default:
		session.state = GitHubPublishAuthError
		session.errorCode = code
		session.error = description
		session.action = "Check the GitHub OAuth App, account permissions, and network, then retry."
		session.retryable = status >= 500 || status == http.StatusTooManyRequests
		session.clearTransient()
		session.wipeTokens()
	}
	model := service.sessionSnapshotLocked(session)
	session.mu.Unlock()
	if code == "authorization_pending" || code == "slow_down" {
		return model, nil
	}
	return model, errors.New(model.Error)
}

func (service *githubPublishService) verifyGitHubAccount(ctx context.Context, accessToken, tokenScope string) (string, []string, error) {
	status, headers, body, err := service.doHTTP(ctx, http.MethodGet, service.apiBaseURL+"/user", nil, accessToken, true)
	if err != nil {
		return "", nil, err
	}
	if status != http.StatusOK {
		if status == http.StatusForbidden {
			return "", nil, githubPublishCodedError("insufficient_permission", "GitHub denied account verification; check account access, organization policy, or rate limits.")
		}
		return "", nil, githubPublishHTTPFailure(status, body)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil || !githubPublishValidOwner(user.Login) {
		return "", nil, errors.New("GitHub returned an invalid authenticated account")
	}
	scopeText := strings.TrimSpace(tokenScope)
	if headerScope := strings.TrimSpace(headers.Get("X-OAuth-Scopes")); headerScope != "" {
		if scopeText == "" {
			scopeText = headerScope
		} else {
			scopeText += "," + headerScope
		}
	}
	scopes := githubPublishScopes(scopeText)
	if !githubPublishHasScope(scopes, "repo") {
		return "", nil, githubPublishCodedError("insufficient_permission", "GitHub granted fewer permissions than required; the repo scope is required to create and push a private repository.")
	}
	return user.Login, scopes, nil
}

func githubPublishCodedError(code, message string) error {
	return &githubPublishError{code: code, message: message}
}

type githubPublishError struct {
	code    string
	message string
}

func (failure *githubPublishError) Error() string {
	if failure == nil {
		return "GitHub publish error"
	}
	return failure.message
}

func githubPublishErrorCode(err error) string {
	var coded *githubPublishError
	if errors.As(err, &coded) {
		return coded.code
	}
	return ""
}

func (service *githubPublishService) cancelSession(session *githubPublishSession, code, message, action string) {
	session.cancel()
	session.mu.Lock()
	session.state = GitHubPublishAuthCancelled
	session.errorCode = code
	session.error = message
	session.action = action
	session.retryable = true
	session.clearTransient()
	session.wipeTokens()
	session.mu.Unlock()
}

func (service *githubPublishService) setSessionError(session *githubPublishSession, code, message, action string, retryable bool) {
	session.mu.Lock()
	if session.state == GitHubPublishAuthCancelled || session.state == GitHubPublishAuthExpired || session.state == GitHubPublishAuthSessionEnded {
		session.mu.Unlock()
		return
	}
	session.state = GitHubPublishAuthError
	session.errorCode = code
	session.error = githubPublishSafeText(message)
	session.action = githubPublishSafeText(action)
	session.retryable = retryable
	session.clearTransient()
	session.wipeTokens()
	session.mu.Unlock()
}

func (service *githubPublishService) expireSessionIfNeeded(session *githubPublishSession) bool {
	if session == nil {
		return false
	}
	now := service.now()
	session.mu.Lock()
	if session.state != GitHubPublishAuthAuthenticated || session.tokenExpiresAt.IsZero() || now.Before(session.tokenExpiresAt) {
		session.mu.Unlock()
		return false
	}
	session.state = GitHubPublishAuthExpired
	session.errorCode = "expired_token"
	session.error = "The GitHub authentication session expired; no further GitHub operation may use its token."
	session.action = "Start a fresh GitHub device authorization."
	session.retryable = true
	session.clearTransient()
	session.wipeTokens()
	session.mu.Unlock()
	service.removeSessionAndPlans(session)
	return true
}

func (service *githubPublishService) removeSessionAndPlans(session *githubPublishSession) {
	if session == nil {
		return
	}
	service.mu.Lock()
	if service.sessions[session.id] == session {
		delete(service.sessions, session.id)
	}
	for planID, plan := range service.plans {
		if plan.sessionID == session.id {
			delete(service.plans, planID)
		}
	}
	prefix := strings.ToLower(session.id) + "\x00"
	for key := range service.uncertainCreates {
		if strings.HasPrefix(key, prefix) {
			delete(service.uncertainCreates, key)
		}
	}
	service.mu.Unlock()
}

func (service *githubPublishService) lookupSession(id string) (*githubPublishSession, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("GitHub authentication session ID is required")
	}
	service.mu.Lock()
	session := service.sessions[id]
	service.mu.Unlock()
	if session == nil {
		return nil, errors.New("GitHub authentication session was not found")
	}
	service.expireSessionIfNeeded(session)
	return session, nil
}

func (service *githubPublishService) sessionSnapshot(session *githubPublishSession) GitHubPublishAuthSession {
	session.mu.Lock()
	defer session.mu.Unlock()
	return service.sessionSnapshotLocked(session)
}

func (service *githubPublishService) sessionSnapshotLocked(session *githubPublishSession) GitHubPublishAuthSession {
	expiresAt := session.expiresAt
	expiresInSeconds := session.expiresInSeconds
	if session.state == GitHubPublishAuthAuthenticated && !session.tokenExpiresAt.IsZero() {
		expiresAt = session.tokenExpiresAt
		remaining := int(session.tokenExpiresAt.Sub(service.now()) / time.Second)
		if remaining < 0 {
			remaining = 0
		}
		expiresInSeconds = remaining
	}
	model := GitHubPublishAuthSession{
		SessionID:           session.id,
		Configured:          githubPublishClientID() != "",
		State:               session.state,
		Login:               session.login,
		UserCode:            session.userCode,
		VerificationURI:     session.verificationURI,
		ExpiresInSeconds:    expiresInSeconds,
		PollIntervalSeconds: int(session.interval / time.Second),
		GrantedScopes:       append([]string(nil), session.grantedScopes...),
		ErrorCode:           session.errorCode,
		Error:               githubPublishSafeText(session.error),
		Action:              githubPublishSafeText(session.action),
		Retryable:           session.retryable,
	}
	if !expiresAt.IsZero() {
		model.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	if !session.nextPollAt.IsZero() {
		model.NextPollAt = session.nextPollAt.UTC().Format(time.RFC3339)
	}
	return model
}

func (service *githubPublishService) Preflight(ctx context.Context, root string, draft GitHubPublishDraft) (GitHubPublishPreflight, error) {
	session, err := service.lookupSession(draft.SessionID)
	if err != nil {
		return GitHubPublishPreflight{SessionID: strings.TrimSpace(draft.SessionID), Error: err.Error(), Action: "Start GitHub device authorization first."}, err
	}
	if service.expireSessionIfNeeded(session) {
		model := service.sessionSnapshot(session)
		return GitHubPublishPreflight{SessionID: session.id, Error: model.Error, Action: model.Action, Retryable: true}, errors.New(model.Error)
	}
	session.mu.Lock()
	login := session.login
	token := session.accessToken
	generation := session.pollGeneration
	sessionContext := session.ctx
	authenticated := session.state == GitHubPublishAuthAuthenticated && token != "" && login != "" && session.ctx.Err() == nil && service.rootCtx.Err() == nil && (session.tokenExpiresAt.IsZero() || service.now().Before(session.tokenExpiresAt))
	session.mu.Unlock()
	if !authenticated {
		return GitHubPublishPreflight{SessionID: session.id, Error: "GitHub authentication is not ready.", Action: "Complete GitHub device authorization and account verification first.", Retryable: true}, errors.New("GitHub authentication is not ready")
	}
	preflightCtx, release := githubPublishLinkedContext(ctx, sessionContext)
	defer release()
	name, description, visibility, validationErr := githubPublishNormalizeDraft(draft)
	if validationErr != nil {
		return GitHubPublishPreflight{SessionID: session.id, Owner: login, Name: name, Description: description, Visibility: visibility, VisibilityOptions: []string{"private", "public"}, Error: validationErr.Error(), Action: "Choose a valid repository name and exactly Private or Public."}, validationErr
	}
	normalizedRoot, err := normalizeGitRoot(root)
	if err != nil {
		return GitHubPublishPreflight{SessionID: session.id, Owner: login, Name: name, Description: description, Visibility: visibility, VisibilityOptions: []string{"private", "public"}, Error: "The active location is not a usable Git repository.", Action: "Open a conventional Git repository with a local main branch and retry."}, err
	}
	local, err := service.captureLocal(preflightCtx, normalizedRoot)
	if err != nil {
		return GitHubPublishPreflight{SessionID: session.id, Owner: login, Name: name, Description: description, Visibility: visibility, VisibilityOptions: []string{"private", "public"}, Local: local.model, Error: githubPublishSafeError(err), Action: "Ensure the repository has a local main branch and no in-progress Git operation, then retry.", Retryable: true}, err
	}
	target, status, targetErr := service.getRepository(preflightCtx, token, login, name)
	preflight := GitHubPublishPreflight{
		SessionID:                  session.id,
		Visibility:                 visibility,
		VisibilityOptions:          []string{"private", "public"},
		Owner:                      login,
		Name:                       name,
		Description:                description,
		TargetURL:                  githubPublishHTMLURL(login, name),
		Local:                      local.model,
		RequiresFinalConfirmation:  true,
		RequiresPublicConfirmation: visibility == "public",
		ProposedRemoteURL:          githubPublishCloneURL(login, name),
		Ready:                      true,
	}
	for index := range local.remotes {
		if strings.EqualFold(local.remotes[index].name, "origin") {
			origin := githubPublishRemoteModel(local.remotes[index], login, name)
			preflight.ExistingOrigin = &origin
			break
		}
	}
	if targetErr != nil && status != http.StatusNotFound {
		preflight.Target = GitHubPublishTarget{Owner: login, Name: name, URL: preflight.TargetURL, State: "error", AccessKnown: status != 0, Error: githubPublishSafeError(targetErr), Action: githubPublishTargetAction(status)}
		preflight.Ready = false
		preflight.Retryable = status == http.StatusTooManyRequests || status >= 500
		preflight.Error = preflight.Target.Error
		preflight.Action = preflight.Target.Action
		return preflight, targetErr
	}
	if status == http.StatusOK && target != nil {
		preflight.Target = githubPublishTargetModel(target, login, name)
		preflight.Target.State = "existing_accessible"
		preflight.Target.Exists = true
		preflight.Target.AccessKnown = true
		preflight.Target.Accessible = true
		if target.Private {
			preflight.Visibility = "private"
		} else {
			preflight.Visibility = "public"
		}
		preflight.RequiresPublicConfirmation = preflight.Visibility == "public"
		preflight.RequiresConnectConfirmation = true
		if !target.CanPush {
			preflight.Ready = false
			preflight.Target.State = "existing_no_push"
			preflight.Target.Error = "The requested GitHub repository exists, but this account cannot push to it."
			preflight.Target.Action = "Choose a repository you can push to, or grant push permission and run preflight again."
			preflight.Error = preflight.Target.Error
			preflight.Action = preflight.Target.Action
		} else {
			preflight.Action = "This repository already exists. Explicitly confirm connecting to it; no GitHub metadata will be changed."
		}
	} else {
		preflight.Target = GitHubPublishTarget{Owner: login, Name: name, URL: preflight.TargetURL, CloneURL: preflight.ProposedRemoteURL, State: "absent_or_inaccessible", AccessKnown: false, Accessible: false, Exists: false}
		preflight.Action = "GitHub did not confirm an accessible target. A confirmed personal-account create may proceed; a 404 is not proof that nobody owns the name."
	}
	for index := range preflight.Local.Remotes {
		preflight.Local.Remotes[index].MatchesTarget = githubPublishRemoteMatchesTarget(local.remotes[index], login, name)
		if preflight.ExistingOrigin != nil && strings.EqualFold(preflight.Local.Remotes[index].Name, "origin") {
			preflight.ExistingOrigin.MatchesTarget = preflight.Local.Remotes[index].MatchesTarget
		}
	}
	uncertainKey := githubPublishUncertainKey(session.id, normalizedRoot, login, name)
	service.mu.Lock()
	_, preflight.CreationUnknown = service.uncertainCreates[uncertainKey]
	service.mu.Unlock()
	if preflight.CreationUnknown {
		preflight.Target.State = "creation_unknown"
		preflight.ReconciliationRequired = true
		preflight.Retryable = true
		preflight.Action = "A previous create response was uncertain. Reconcile with GitHub; retry creation only after a fresh explicit confirmation."
	}
	remoteName, _ := githubPublishChooseRemote(local.remotes, login, name)
	preflight.ProposedRemoteName = remoteName
	plan := &githubPublishPlan{id: uuid.NewString(), sessionID: session.id, workspaceID: strings.TrimSpace(draft.WorkspaceID), root: normalizedRoot, owner: login, name: name, description: description, visibility: preflight.Visibility, localOID: local.mainOID, local: local, target: target, targetState: preflight.Target.State, creationUnknown: preflight.CreationUnknown, createdAt: service.now()}
	if preflightCtx.Err() != nil {
		preflight.Ready = false
		preflight.Error = "GitHub preflight was cancelled before its session could be committed."
		preflight.Action = "Start preflight again while GitHub authentication is active."
		return preflight, preflightCtx.Err()
	}
	service.mu.Lock()
	live := !service.closed && service.sessions[session.id] == session
	if live {
		session.mu.Lock()
		live = session.state == GitHubPublishAuthAuthenticated && session.pollGeneration == generation && session.accessToken == token && session.ctx.Err() == nil && service.rootCtx.Err() == nil && preflightCtx.Err() == nil && (session.tokenExpiresAt.IsZero() || service.now().Before(session.tokenExpiresAt))
		session.mu.Unlock()
	}
	if live {
		service.plans[plan.id] = plan
	}
	service.mu.Unlock()
	if !live {
		service.expireSessionIfNeeded(session)
	}
	if !live {
		preflight.Ready = false
		preflight.Error = "GitHub authentication ended while preflight was running; no publish plan was stored."
		preflight.Action = "Authenticate again and run preflight again."
		return preflight, errors.New(preflight.Error)
	}
	return preflight, nil
}
func (service *githubPublishService) runOperation(operation *githubPublishOperation, session *githubPublishSession, request GitHubPublishStartRequest, operationCtx context.Context) {
	defer close(operation.done)
	defer operation.cancel()
	defer func() {
		session.mu.Lock()
		delete(session.operationIDs, operation.model.OperationID)
		session.wipeTokens()
		session.clearTransient()
		if session.state == GitHubPublishAuthAuthenticated {
			session.state = GitHubPublishAuthSessionEnded
			session.errorCode = "session_ended"
			session.error = "The short-lived GitHub session ended after this publish operation."
			session.action = "Authenticate again before starting another publish."
			session.retryable = true
		}
		session.mu.Unlock()
	}()
	service.setOperationState(operation, GitHubPublishOperationRunning, false, false, false, "")
	root := operation.plan.root
	var workspaceUnlock func()
	if service.workspaceLock != nil && operation.plan.workspaceID != "" {
		workspaceMutex := service.workspaceLock(operation.plan.workspaceID)
		if lockErr := lockMutexContext(operationCtx, workspaceMutex); lockErr != nil {
			service.finishCancelledOperation(operation, lockErr)
			return
		}
		workspaceUnlock = workspaceMutex.Unlock
		defer workspaceUnlock()
	}
	var resultErr error
	resultErr = service.git.withRemoteGate(operationCtx, root, func(gitCtx context.Context) error {
		if err := service.checkOperationCancelled(gitCtx); err != nil {
			return err
		}
		service.runOperationStep(operation, 1)
		local, err := service.captureLocalLocked(gitCtx, root)
		if err != nil {
			service.failOperationStep(operation, 1, err, true, false)
			return err
		}
		if local.mainOID != operation.plan.localOID {
			err = errors.New("local main changed after preflight; run preflight again before publishing")
			service.failOperationStep(operation, 1, err, true, true)
			return err
		}
		service.succeedOperationStep(operation, 1, "Local repository, main commit, and remotes revalidated.")

		if service.expireSessionIfNeeded(session) {
			err = errors.New("GitHub authentication session expired before operation start")
			service.failOperationStep(operation, 2, err, true, true)
			return err
		}
		service.runOperationStep(operation, 2)
		session.mu.Lock()
		token := session.accessToken
		login := session.login
		session.mu.Unlock()
		defer func() { token = ""; login = "" }()
		repo, status, targetErr := service.getRepository(gitCtx, token, login, operation.plan.name)
		if targetErr != nil && status != http.StatusNotFound {
			service.failOperationStep(operation, 2, targetErr, status >= 500 || status == http.StatusTooManyRequests, false)
			return targetErr
		}
		if operation.plan.targetState == "existing_accessible" {
			if status != http.StatusOK || repo == nil {
				err = errors.New("the existing GitHub target changed during preflight; run preflight again")
				service.failOperationStep(operation, 2, err, true, true)
				return err
			}
			if !githubPublishTargetBindingMatches(operation.plan, repo) {
				err = errors.New("the confirmed GitHub repository identity or visibility changed after preflight; run preflight again")
				service.failOperationStep(operation, 2, err, true, true)
				return err
			}
			if !repo.CanPush {
				err = errors.New("the existing GitHub repository is no longer pushable by this account")
				service.failOperationStep(operation, 2, err, false, false)
				return err
			}
		} else if status == http.StatusOK {
			err = errors.New("the requested GitHub name now resolves to an existing repository; run preflight and explicitly confirm connect")
			service.failOperationStep(operation, 2, err, true, true)
			return err
		}
		service.succeedOperationStep(operation, 2, "GitHub target access and collision state revalidated.")

		service.runOperationStep(operation, 3)
		var resolved *githubPublishRepo
		if status == http.StatusOK && repo != nil {
			resolved = repo
			service.setOperationRepository(operation, repo, false)
			service.succeedOperationStep(operation, 3, "Connected to the explicitly confirmed existing GitHub repository.")
		} else {
			if operation.plan.creationUnknown && !request.RetryUnknownCreation {
				err = errors.New("creation remains unresolved; no duplicate GitHub create was attempted")
				service.failOperationStep(operation, 3, err, true, true)
				return err
			}
			session.mu.Lock()
			token = session.accessToken
			session.mu.Unlock()
			created, createStatus, createErr := service.createRepository(gitCtx, token, operation.plan.owner, operation.plan.name, operation.plan.description, operation.plan.visibility)
			if createErr != nil {
				resolved, uncertainErr := service.reconcileAfterCreateFailure(token, operation.plan.owner, operation.plan.name, createStatus, createErr)
				if resolved != nil {
					service.setOperationRepository(operation, resolved, true)
					if operationCtx.Err() != nil {
						service.failOperationStep(operation, 3, context.Canceled, true, true)
						return operationCtx.Err()
					}
					err = errors.New("GitHub repository creation may have succeeded; explicit connect confirmation is required before local mutation")
					service.failOperationStep(operation, 3, err, true, true)
					service.setOperationNeedsConfirmation(operation, err, resolved.HTMLURL)
					return err
				}
				if uncertainErr != nil {
					service.markCreationUnknown(operation, uncertainErr)
					return uncertainErr
				}
				service.failOperationStep(operation, 3, createErr, createStatus >= 500 || createStatus == http.StatusTooManyRequests, false)
				return createErr
			}
			resolved = created
			service.setOperationRepository(operation, resolved, true)
			service.succeedOperationStep(operation, 3, "GitHub confirmed the requested repository identity, metadata, visibility, and clean URL.")
		}

		if err := service.checkOperationCancelled(gitCtx); err != nil {
			return err
		}
		service.runOperationStep(operation, 4)
		remotes, remotesErr := service.readPublishRemotesLocked(gitCtx, root)
		if remotesErr != nil {
			service.failOperationStep(operation, 4, remotesErr, true, false)
			return remotesErr
		}
		remoteName, remoteToReuse := githubPublishChooseRemote(remotes, operation.plan.owner, operation.plan.name)
		remoteURL := resolved.CloneURL
		if remoteToReuse != nil {
			if !githubPublishRemoteURLsSafe(remoteToReuse) {
				err = errors.New("the existing GitHub remote contains embedded credentials or URL parameters")
				service.failOperationStep(operation, 4, err, false, false)
				return err
			}
			service.setOperationRemote(operation, remoteName, remoteURL, false, true)
		} else {
			_, _, commandErr := service.git.command(gitCtx, root, "remote", "add", remoteName, remoteURL)
			if commandErr != nil {
				service.setOperationRemoteUnknown(operation, remoteName, remoteURL)
				after, rereadErr := service.reconcilePublishRemotes(root)
				if rereadErr == nil {
					afterName, exact := githubPublishChooseRemote(after, operation.plan.owner, operation.plan.name)
					if exact != nil {
						service.setOperationRemoteUnknown(operation, afterName, remoteURL)
					}
				}
				wrapped := commandFailure("add GitHub remote", "", "", commandErr)
				if rereadErr != nil {
					wrapped = fmt.Errorf("%w; local remote state could not be reconciled", wrapped)
				} else {
					wrapped = fmt.Errorf("%w; the remote-add result is ambiguous and requires reconciliation", wrapped)
				}
				service.failOperationStep(operation, 4, wrapped, true, true)
				return wrapped
			} else {
				service.setOperationRemote(operation, remoteName, remoteURL, true, false)
			}
		}
		verifiedRemotes, verifyRemoteErr := service.readPublishRemotesLocked(gitCtx, root)
		if verifyRemoteErr != nil {
			service.failOperationStep(operation, 4, verifyRemoteErr, true, false)
			return verifyRemoteErr
		}
		actual := githubPublishFindRemote(verifiedRemotes, remoteName)
		if actual == nil || !githubPublishRemoteMatchesTarget(*actual, operation.plan.owner, operation.plan.name) || !githubPublishRemoteURLsSafe(actual) {
			err = errors.New("the selected GitHub remote could not be verified without changing existing remotes")
			service.failOperationStep(operation, 4, err, true, false)
			return err
		}
		service.succeedOperationStep(operation, 4, "GitHub remote was reused or added without retargeting existing remotes.")

		if err := service.checkOperationCancelled(gitCtx); err != nil {
			return err
		}
		service.runOperationStep(operation, 5)
		localOID := operation.plan.localOID
		stdout, stderr, pushErr := service.git.command(gitCtx, root, "push", "-u", remoteName, "main")
		if pushErr != nil {
			remoteOID, lsErr := service.reconcileRemoteMain(remoteName, root, gitCtx)
			if lsErr == nil && remoteOID == localOID {
				service.setOperationPushState(operation, "succeeded")
				service.succeedOperationStep(operation, 5, "GitHub already contains the local main commit after the push response.")
			} else if lsErr == nil && remoteOID != "" && remoteOID != localOID {
				service.setOperationPushState(operation, "conflict")
				err = errors.New("GitHub main points to a different commit; fetch and merge manually instead of force-pushing")
				service.failOperationStep(operation, 5, err, false, false)
				return err
			} else {
				service.setOperationPushState(operation, "unknown")
				wrapped := commandFailure("push GitHub main", stdout, stderr, pushErr)
				service.failOperationStep(operation, 5, wrapped, true, true)
				return wrapped
			}
		} else {
			service.setOperationPushState(operation, "succeeded")
			service.succeedOperationStep(operation, 5, "Local main was pushed with upstream setup using conventional Git credentials.")
		}

		if err := service.checkOperationCancelled(gitCtx); err != nil {
			return err
		}
		service.runOperationStep(operation, 6)
		if verifyErr := service.verifyUpstreamAndRemote(gitCtx, root, remoteName, localOID); verifyErr != nil {
			service.failOperationStep(operation, 6, verifyErr, true, false)
			return verifyErr
		}
		service.setOperationUpstream(operation, remoteName, "refs/heads/main")
		service.succeedOperationStep(operation, 6, "main upstream and GitHub main commit OID were verified.")
		service.runOperationStep(operation, 7)
		service.succeedOperationStep(operation, 7, "GitHub publish completed.")
		return nil
	})
	if resultErr != nil {
		if errors.Is(resultErr, context.Canceled) || errors.Is(operationCtx.Err(), context.Canceled) {
			service.finishCancelledOperation(operation, resultErr)
		} else {
			service.finishFailedOperation(operation, resultErr)
		}
		return
	}
	service.finishCompleteOperation(operation)
}

func (service *githubPublishService) checkOperationCancelled(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (service *githubPublishService) runOperationStep(operation *githubPublishOperation, index int) {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if index >= 0 && index < len(operation.model.Steps) {
		operation.model.Steps[index].State = GitHubPublishStepRunning
		operation.model.Steps[index].Attempt++
	}
}

func (service *githubPublishService) succeedOperationStep(operation *githubPublishOperation, index int, message string) {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if index >= 0 && index < len(operation.model.Steps) {
		operation.model.Steps[index].State = GitHubPublishStepSucceeded
		operation.model.Steps[index].Message = githubPublishSafeText(message)
	}
}

func (service *githubPublishService) failOperationStep(operation *githubPublishOperation, index int, err error, retryable, reconcile bool) {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	if index >= 0 && index < len(operation.model.Steps) {
		operation.model.Steps[index].State = GitHubPublishStepFailed
		operation.model.Steps[index].Message = githubPublishSafeError(err)
	}
	operation.model.Error = githubPublishSafeError(err)
	operation.model.Action = githubPublishActionForError(err)
	operation.model.Retryable = retryable
	operation.model.ReconciliationRequired = reconcile
}

func (service *githubPublishService) setOperationState(operation *githubPublishOperation, state string, retryable, reconcile, cancelled bool, errMessage string) {
	operation.mu.Lock()
	operation.model.State = state
	operation.model.Retryable = retryable
	operation.model.ReconciliationRequired = reconcile
	operation.model.Cancelled = cancelled
	if errMessage != "" {
		operation.model.Error = githubPublishSafeText(errMessage)
		operation.model.Action = githubPublishActionForError(errors.New(errMessage))
	}
	operation.mu.Unlock()
}

func githubPublishTargetBindingMatches(plan *githubPublishPlan, repo *githubPublishRepo) bool {
	return plan != nil && plan.target != nil && repo != nil && repo.ID == plan.target.ID && repo.Private == plan.target.Private
}

func (service *githubPublishService) setOperationRepository(operation *githubPublishOperation, repo *githubPublishRepo, created bool) {
	operation.mu.Lock()
	operation.model.Partial.RepositoryURL = repo.HTMLURL
	if created {
		operation.model.Partial.CreatedURL = repo.HTMLURL
	}
	operation.mu.Unlock()
}

func (service *githubPublishService) setOperationNeedsConfirmation(operation *githubPublishOperation, err error, urlValue string) {
	operation.mu.Lock()
	operation.model.State = GitHubPublishOperationNeedsConfirm
	operation.model.Retryable = true
	operation.model.ReconciliationRequired = true
	operation.model.Error = githubPublishSafeError(err)
	operation.model.Action = "Run preflight and explicitly confirm connecting to the existing GitHub repository before changing local Git."
	operation.model.Partial.RepositoryURL = urlValue
	operation.mu.Unlock()
}

func (service *githubPublishService) markCreationUnknown(operation *githubPublishOperation, err error) {
	service.failOperationStep(operation, 3, err, true, true)
	operation.mu.Lock()
	operation.model.State = GitHubPublishOperationUnknown
	operation.model.Retryable = true
	operation.model.ReconciliationRequired = true
	operation.model.Action = "Creation is unknown. Reconcile with GitHub before any retry; never submit a blind duplicate create."
	operation.mu.Unlock()
	service.mu.Lock()
	service.uncertainCreates[githubPublishUncertainKey(operation.plan.sessionID, operation.plan.root, operation.plan.owner, operation.plan.name)] = struct{}{}
	service.mu.Unlock()
}

func (service *githubPublishService) setOperationRemote(operation *githubPublishOperation, name, remoteURL string, added, reused bool) {
	operation.mu.Lock()
	operation.model.Partial.RemoteName = name
	operation.model.Partial.RemoteURL = remoteURL
	operation.model.Partial.RemoteAdded = operation.model.Partial.RemoteAdded || added
	operation.model.Partial.RemoteReused = operation.model.Partial.RemoteReused || reused
	switch {
	case added:
		operation.model.Partial.RemoteState = "added"
	case reused:
		operation.model.Partial.RemoteState = "reused"
	}
	operation.mu.Unlock()
}

func (service *githubPublishService) setOperationRemoteUnknown(operation *githubPublishOperation, name, remoteURL string) {
	operation.mu.Lock()
	operation.model.Partial.RemoteName = name
	operation.model.Partial.RemoteURL = remoteURL
	operation.model.Partial.RemoteState = "unknown"
	operation.model.ReconciliationRequired = true
	operation.mu.Unlock()
}

func (service *githubPublishService) setOperationPushState(operation *githubPublishOperation, state string) {
	operation.mu.Lock()
	operation.model.Partial.PushState = state
	operation.mu.Unlock()
}

func (service *githubPublishService) setOperationUpstream(operation *githubPublishOperation, remote, merge string) {
	operation.mu.Lock()
	operation.model.Partial.UpstreamRemote = remote
	operation.model.Partial.UpstreamMerge = merge
	operation.model.Partial.UpstreamVerified = true
	operation.mu.Unlock()
}

func (service *githubPublishService) finishCompleteOperation(operation *githubPublishOperation) {
	operation.mu.Lock()
	operation.model.State = GitHubPublishOperationComplete
	operation.model.Completed = true
	operation.model.Error = ""
	operation.model.Action = ""
	operation.model.Retryable = false
	operation.model.ReconciliationRequired = false
	operation.mu.Unlock()
}

func (service *githubPublishService) finishFailedOperation(operation *githubPublishOperation, err error) {
	operation.mu.Lock()
	if operation.model.State != GitHubPublishOperationNeedsConfirm && operation.model.State != GitHubPublishOperationUnknown {
		operation.model.State = GitHubPublishOperationFailed
	}
	operation.model.Error = githubPublishSafeError(err)
	if operation.model.Action == "" {
		operation.model.Action = githubPublishActionForError(err)
	}
	operation.model.Completed = true
	operation.mu.Unlock()
}

func (service *githubPublishService) finishCancelledOperation(operation *githubPublishOperation, err error) {
	operation.mu.Lock()
	operation.model.State = GitHubPublishOperationCancelled
	operation.model.Cancelled = true
	operation.model.Completed = true
	operation.model.Error = "GitHub publish was cancelled."
	operation.model.Action = "Review the partial state before retrying; no rollback or destructive GitHub operation was attempted."
	operation.model.Retryable = true
	for index := range operation.model.Steps {
		if operation.model.Steps[index].State == GitHubPublishStepRunning || operation.model.Steps[index].State == GitHubPublishStepPending {
			operation.model.Steps[index].State = GitHubPublishStepCancelled
		}
	}
	operation.mu.Unlock()
	_ = err
}

func (service *githubPublishService) operationSnapshot(operation *githubPublishOperation) GitHubPublishOperation {
	operation.mu.Lock()
	defer operation.mu.Unlock()
	model := operation.model
	model.Steps = append([]GitHubPublishStep(nil), operation.model.Steps...)
	return model
}

func (service *githubPublishService) GetOperation(operationID string) (GitHubPublishOperation, error) {
	service.mu.Lock()
	operation := service.operations[strings.TrimSpace(operationID)]
	service.mu.Unlock()
	if operation == nil {
		return GitHubPublishOperation{}, errors.New("GitHub publish operation was not found")
	}
	return service.operationSnapshot(operation), nil
}

func (service *githubPublishService) CancelAuth(sessionID string) (GitHubPublishAuthSession, error) {
	session, err := service.lookupSession(sessionID)
	if err != nil {
		return GitHubPublishAuthSession{SessionID: strings.TrimSpace(sessionID), State: GitHubPublishAuthError, ErrorCode: "unknown_session", Error: "GitHub authentication session was not found.", Action: "Start a new GitHub sign-in."}, err
	}
	service.cancelSession(session, "authentication_cancelled", "Authentication was cancelled.", "Start a fresh GitHub sign-in if you still want to publish.")
	return service.sessionSnapshot(session), nil
}

func (service *githubPublishService) CancelOperation(operationID string) (GitHubPublishOperation, error) {
	service.mu.Lock()
	operation := service.operations[strings.TrimSpace(operationID)]
	service.mu.Unlock()
	if operation == nil {
		return GitHubPublishOperation{}, errors.New("GitHub publish operation was not found")
	}
	operation.cancel()
	return service.operationSnapshot(operation), nil
}

func (service *githubPublishService) ClearSession(sessionID string) error {
	session, err := service.lookupSession(sessionID)
	if err != nil {
		return err
	}
	session.cancel()
	session.mu.Lock()
	operationIDs := make([]string, 0, len(session.operationIDs))
	for operationID := range session.operationIDs {
		operationIDs = append(operationIDs, operationID)
	}
	session.state = GitHubPublishAuthCancelled
	session.errorCode = "session_cleared"
	session.error = "The GitHub authentication session was cleared."
	session.action = "Start a fresh GitHub sign-in to publish."
	session.retryable = true
	session.clearTransient()
	session.wipeTokens()
	session.mu.Unlock()
	for _, operationID := range operationIDs {
		service.mu.Lock()
		operation := service.operations[operationID]
		service.mu.Unlock()
		if operation != nil {
			operation.cancel()
		}
	}
	service.mu.Lock()
	delete(service.sessions, session.id)
	for planID, plan := range service.plans {
		if plan.sessionID == session.id {
			delete(service.plans, planID)
		}
	}
	service.mu.Unlock()
	return nil
}

func (service *githubPublishService) Shutdown() {
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		return
	}
	service.closed = true
	sessions := make([]*githubPublishSession, 0, len(service.sessions))
	for _, session := range service.sessions {
		sessions = append(sessions, session)
	}
	operations := make([]*githubPublishOperation, 0, len(service.operations))
	for _, operation := range service.operations {
		operations = append(operations, operation)
	}
	service.mu.Unlock()
	service.cancel()
	for _, session := range sessions {
		session.cancel()
		session.mu.Lock()
		session.clearTransient()
		session.wipeTokens()
		session.state = GitHubPublishAuthCancelled
		session.errorCode = "shutdown"
		session.error = "GitHub publishing was cancelled during application shutdown."
		session.action = "Restart Mod Studio to publish again."
		session.mu.Unlock()
	}
	for _, operation := range operations {
		operation.cancel()
	}
}

func (service *githubPublishService) Start(ctx context.Context, request GitHubPublishStartRequest) (GitHubPublishOperation, error) {
	request.PlanID = strings.TrimSpace(request.PlanID)
	request.SessionID = strings.TrimSpace(request.SessionID)
	service.mu.Lock()
	plan := service.plans[request.PlanID]
	service.mu.Unlock()
	if plan == nil || plan.sessionID != request.SessionID {
		return GitHubPublishOperation{}, errors.New("GitHub publish preflight plan was not found; run preflight again")
	}
	session, err := service.lookupSession(plan.sessionID)
	if err != nil {
		return GitHubPublishOperation{}, err
	}
	if service.expireSessionIfNeeded(session) {
		model := service.sessionSnapshot(session)
		return GitHubPublishOperation{}, errors.New(model.Error)
	}
	session.mu.Lock()
	authenticated := session.state == GitHubPublishAuthAuthenticated && session.accessToken != "" && (session.tokenExpiresAt.IsZero() || service.now().Before(session.tokenExpiresAt))
	session.mu.Unlock()
	if !authenticated {
		return GitHubPublishOperation{}, errors.New("GitHub authentication session is no longer active; sign in again")
	}
	if !request.Confirmed {
		return GitHubPublishOperation{}, errors.New("final GitHub publish confirmation is required")
	}
	if plan.visibility == "public" && !request.PublicConfirmed {
		return GitHubPublishOperation{}, errors.New("public repository creation requires a second explicit Public confirmation")
	}
	if plan.targetState == "existing_accessible" || plan.targetState == "existing_no_push" {
		if !request.ConnectExisting {
			return GitHubPublishOperation{}, errors.New("connecting to an existing GitHub repository requires explicit confirmation")
		}
		if plan.targetState == "existing_no_push" {
			return GitHubPublishOperation{}, errors.New("the existing GitHub repository is not pushable by this account")
		}
	}
	if plan.creationUnknown && !request.RetryUnknownCreation {
		return GitHubPublishOperation{}, errors.New("creation is unresolved; reconcile the target or explicitly confirm a retry")
	}

	baseCtx, baseCancel := context.WithCancel(session.ctx)
	opCtx := baseCtx
	release := func() {}
	if ctx != nil {
		var linked context.Context
		linked, release = githubPublishLinkedContext(ctx, baseCtx)
		opCtx = linked
	}
	operation := &githubPublishOperation{
		cancel: func() {
			baseCancel()
			release()
		},
		release: release,
		done:    make(chan struct{}),
		plan:    plan,
		model: GitHubPublishOperation{
			OperationID: request.PlanID + "-" + uuid.NewString(),
			SessionID:   plan.sessionID,
			State:       GitHubPublishOperationQueued,
			Steps:       githubPublishInitialSteps(),
			Partial:     GitHubPublishPartialState{RemoteState: "not_started", PushState: "not_started"},
		},
	}
	session.mu.Lock()
	if session.state != GitHubPublishAuthAuthenticated || session.accessToken == "" {
		session.mu.Unlock()
		operation.cancel()
		return GitHubPublishOperation{}, errors.New("GitHub authentication session is no longer active; sign in again")
	}
	session.operationIDs[operation.model.OperationID] = struct{}{}
	session.mu.Unlock()
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		session.mu.Lock()
		delete(session.operationIDs, operation.model.OperationID)
		session.mu.Unlock()
		operation.cancel()
		return GitHubPublishOperation{}, errors.New("GitHub publishing service is closed")
	}
	service.operations[operation.model.OperationID] = operation
	service.mu.Unlock()
	go service.runOperation(operation, session, request, opCtx)
	return service.operationSnapshot(operation), nil
}

func githubPublishLinkedContext(ctx, parent context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	linked, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, cancel)
	return linked, func() {
		stop()
		cancel()
	}
}

func githubPublishInitialSteps() []GitHubPublishStep {
	return []GitHubPublishStep{
		{Name: "authentication", State: GitHubPublishStepSucceeded, Message: "GitHub account authenticated and verified."},
		{Name: "local_validation", State: GitHubPublishStepPending},
		{Name: "target_preflight", State: GitHubPublishStepPending},
		{Name: "create_or_connect", State: GitHubPublishStepPending},
		{Name: "remote_setup", State: GitHubPublishStepPending},
		{Name: "push", State: GitHubPublishStepPending},
		{Name: "upstream_verification", State: GitHubPublishStepPending},
		{Name: "completion", State: GitHubPublishStepPending},
	}
}

func (service *githubPublishService) captureLocal(ctx context.Context, root string) (githubPublishLocalSnapshot, error) {
	var snapshot githubPublishLocalSnapshot
	err := service.git.withGate(ctx, root, func() error {
		var err error
		snapshot, err = service.captureLocalLocked(ctx, root)
		return err
	})
	return snapshot, err
}

func (service *githubPublishService) captureLocalLocked(ctx context.Context, root string) (githubPublishLocalSnapshot, error) {
	var snapshot githubPublishLocalSnapshot
	snapshot.model.Root = root
	repository, repositoryErr := service.git.repositoryState(ctx, root)
	if repositoryErr != nil {
		return snapshot, repositoryErr
	}
	if !repository {
		return snapshot, errors.New("active location is not a Git repository")
	}
	snapshot.model.Repository = true
	gitDirOutput, gitDirStderr, gitDirErr := service.git.command(ctx, root, "rev-parse", "--git-dir")
	if gitDirErr != nil {
		return snapshot, commandFailure("inspect Git metadata", gitDirOutput, gitDirStderr, gitDirErr)
	}
	gitDir := strings.TrimSpace(gitDirOutput)
	if gitDir == "" {
		return snapshot, errors.New("Git returned an empty metadata directory")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir, _ = filepath.Abs(filepath.Clean(gitDir))
	snapshot.gitDirectory = gitDir
	snapshot.model.GitDirectory = gitDir
	if githubPublishGitOperationInProgress(gitDir) {
		return snapshot, errors.New("Git has an in-progress operation; finish or abort it before publishing")
	}
	branchOutput, _, branchErr := service.git.command(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr != nil {
		snapshot.model.Detached = true
		return snapshot, errors.New("the active Git repository is detached; check out local main before publishing")
	}
	snapshot.model.CurrentBranch = strings.TrimSpace(branchOutput)
	if snapshot.model.CurrentBranch != "main" {
		return snapshot, errors.New("the active Git branch is not main")
	}
	showRefOutput, _, showRefErr := service.git.command(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/main")
	if showRefErr != nil {
		return snapshot, errors.New("local main does not exist or is unborn")
	}
	_ = showRefOutput
	oidOutput, _, oidErr := service.git.command(ctx, root, "rev-parse", "--verify", "refs/heads/main^{commit}")
	if oidErr != nil {
		return snapshot, errors.New("local main does not resolve to a commit")
	}
	oidLines := strings.Split(strings.ReplaceAll(oidOutput, "\r\n", "\n"), "\n")
	oid := strings.TrimSpace(oidLines[0])
	if !githubPublishValidOID(oid) {
		return snapshot, errors.New("local main resolved to an invalid commit identifier")
	}
	snapshot.mainOID = oid
	snapshot.model.MainOID = oid
	snapshot.model.MainAvailable = true
	remotes, remotesErr := service.readPublishRemotesLocked(ctx, root)
	if remotesErr != nil {
		return snapshot, remotesErr
	}
	snapshot.remotes = remotes
	for _, remote := range remotes {
		snapshot.model.Remotes = append(snapshot.model.Remotes, githubPublishRemoteModel(remote, "", ""))
	}
	return snapshot, nil
}
func (service *githubPublishService) reconcilePublishRemotes(root string) ([]githubPublishRemote, error) {
	reconcileCtx, cancel := context.WithTimeout(service.rootCtx, 5*time.Second)
	defer cancel()
	return service.readPublishRemotesLocked(reconcileCtx, root)
}

func githubPublishGitOperationInProgress(gitDir string) bool {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "sequencer", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return true
		}
	}
	return false
}

func (service *githubPublishService) readPublishRemotesLocked(ctx context.Context, root string) ([]githubPublishRemote, error) {
	stdout, stderr, err := service.git.command(ctx, root, "remote", "-v")
	if err != nil {
		return nil, commandFailure("read Git remotes", stdout, stderr, err)
	}
	byName := make(map[string]int)
	remotes := make([]githubPublishRemote, 0)
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, remoteURL := fields[0], fields[1]
		index, ok := byName[name]
		if !ok {
			index = len(remotes)
			byName[name] = index
			remotes = append(remotes, githubPublishRemote{name: name})
		}
		switch fields[len(fields)-1] {
		case "(fetch)":
			remotes[index].fetchURL = remoteURL
		case "(push)":
			remotes[index].pushURL = remoteURL
		}
	}
	return remotes, nil
}

func (service *githubPublishService) getRepository(ctx context.Context, token, owner, name string) (*githubPublishRepo, int, error) {
	endpoint := service.apiBaseURL + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	status, _, body, err := service.doHTTP(ctx, http.MethodGet, endpoint, nil, token, true)
	if err != nil {
		return nil, 0, err
	}
	if status == http.StatusNotFound {
		return nil, status, nil
	}
	if status != http.StatusOK {
		return nil, status, githubPublishHTTPFailure(status, body)
	}
	repo, parseErr := parseGitHubRepository(body)
	if parseErr != nil {
		return nil, status, parseErr
	}
	validated, validateErr := githubPublishValidateRepository(repo, owner, name, "")
	if validateErr != nil {
		return nil, status, validateErr
	}
	return validated, status, nil
}

func (service *githubPublishService) createRepository(ctx context.Context, token, owner, name, description, visibility string) (*githubPublishRepo, int, error) {
	payload := struct {
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
		Private     bool    `json:"private"`
		AutoInit    bool    `json:"auto_init"`
	}{Name: name, Private: visibility == "private", AutoInit: false}
	if strings.TrimSpace(description) != "" {
		value := description
		payload.Description = &value
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	status, _, responseBody, requestErr := service.doHTTP(ctx, http.MethodPost, service.apiBaseURL+"/user/repos", body, token, true)
	if requestErr != nil {
		return nil, 0, requestErr
	}
	if status != http.StatusCreated {
		return nil, status, githubPublishHTTPFailure(status, responseBody)
	}
	repo, parseErr := parseGitHubRepository(responseBody)
	if parseErr != nil {
		return nil, status, parseErr
	}
	validated, validateErr := githubPublishValidateRepository(repo, owner, name, visibility)
	if validateErr != nil {
		return nil, status, validateErr
	}
	if strings.TrimSpace(description) != strings.TrimSpace(validated.Description) {
		return nil, status, errors.New("GitHub create response did not preserve the confirmed description")
	}
	return validated, status, nil
}

func (service *githubPublishService) reconcileAfterCreateFailure(token, owner, name string, status int, createErr error) (*githubPublishRepo, error) {
	uncertain := status == 0 || status == http.StatusCreated || status >= 500 || errors.Is(createErr, context.Canceled) || errors.Is(createErr, context.DeadlineExceeded)
	if !uncertain && status != http.StatusUnprocessableEntity {
		return nil, nil
	}
	for attempt := 0; attempt < githubPublishMaxCreationReconcileAttempts; attempt++ {
		reconcileCtx, cancel := context.WithTimeout(service.rootCtx, 10*time.Second)
		repo, getStatus, getErr := service.getRepository(reconcileCtx, token, owner, name)
		cancel()
		if repo != nil && getStatus == http.StatusOK {
			return repo, nil
		}
		if getErr != nil && getStatus != http.StatusNotFound && !uncertain {
			return nil, nil
		}
		if attempt+1 < githubPublishMaxCreationReconcileAttempts {
			if sleepErr := service.sleep(service.rootCtx, time.Duration(250*(1<<attempt))*time.Millisecond); sleepErr != nil {
				break
			}
		}
	}
	if uncertain {
		return nil, errors.New("GitHub repository creation response is unknown; reconcile before retrying")
	}
	return nil, nil
}

func (service *githubPublishService) reconcileRemoteMain(remoteName, root string, ctx context.Context) (string, error) {
	stdout, stderr, err := service.git.command(ctx, root, "ls-remote", "--heads", remoteName, "refs/heads/main")
	if err != nil && strings.TrimSpace(stdout) == "" {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if strings.Contains(strings.ToLower(stderr), "could not read") || strings.Contains(strings.ToLower(stderr), "fatal: unable") || strings.Contains(strings.ToLower(stderr), "authentication") {
			return "", commandFailure("reconcile GitHub main", stdout, stderr, err)
		}
		return "", nil
	}
	line := strings.TrimSpace(strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n")[0])
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || !githubPublishValidOID(fields[0]) {
		return "", errors.New("GitHub returned an invalid main commit identifier")
	}
	return fields[0], nil
}

func (service *githubPublishService) verifyUpstreamAndRemote(ctx context.Context, root, remoteName, localOID string) error {
	upstreamOutput, stderr, err := service.git.command(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "main@{upstream}")
	if err != nil {
		return commandFailure("verify main upstream", upstreamOutput, stderr, err)
	}
	if strings.TrimSpace(upstreamOutput) != remoteName+"/main" {
		return errors.New("local main is not tracking the selected GitHub remote main")
	}
	remoteOutput, _, err := service.git.command(ctx, root, "config", "--get", "branch.main.remote")
	if err != nil || strings.TrimSpace(remoteOutput) != remoteName {
		return errors.New("Git branch.main.remote does not match the selected GitHub remote")
	}
	mergeOutput, _, err := service.git.command(ctx, root, "config", "--get", "branch.main.merge")
	if err != nil || strings.TrimSpace(mergeOutput) != "refs/heads/main" {
		return errors.New("Git branch.main.merge is not refs/heads/main")
	}
	lsOutput, stderr, err := service.git.command(ctx, root, "ls-remote", "--heads", remoteName, "refs/heads/main")
	if err != nil {
		return commandFailure("verify GitHub main", lsOutput, stderr, err)
	}
	fields := strings.Fields(lsOutput)
	if len(fields) == 0 || fields[0] != localOID {
		return errors.New("GitHub main does not match the local main commit")
	}
	return nil
}

func githubPublishNormalizeDraft(draft GitHubPublishDraft) (string, string, string, error) {
	name := strings.TrimSpace(draft.Name)
	description := strings.TrimSpace(draft.Description)
	if !githubPublishValidRepositoryName(name) {
		return name, description, "", errors.New("repository name must be 1-100 ASCII letters, digits, '.', '-', or '_'")
	}
	visibility := strings.ToLower(strings.TrimSpace(draft.Visibility))
	if visibility == "" {
		visibility = "private"
	}
	if visibility != "private" && visibility != "public" {
		return name, description, visibility, errors.New("repository visibility must be exactly Private or Public")
	}
	return name, description, visibility, nil
}

var (
	githubPublishRepositoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	githubPublishOwnerPattern          = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	githubPublishOIDPattern            = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)
	githubPublishTokenPattern          = regexp.MustCompile(`(?i)(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|oauth_token_[A-Za-z0-9_]+|v1\.[A-Za-z0-9_-]+)`)
	githubPublishAuthorizationPattern  = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|token)\s+|bearer\s+)[^\s,;]+`)
)

func githubPublishValidRepositoryName(name string) bool {
	return githubPublishRepositoryNamePattern.MatchString(name)
}

func githubPublishValidOwner(owner string) bool {
	return githubPublishOwnerPattern.MatchString(strings.TrimSpace(owner))
}

func githubPublishValidOID(oid string) bool {
	return githubPublishOIDPattern.MatchString(strings.TrimSpace(oid))
}

func githubPublishScopes(value string) []string {
	seen := make(map[string]struct{})
	var scopes []string
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		scopes = append(scopes, part)
	}
	return scopes
}

func githubPublishHasScope(scopes []string, expected string) bool {
	for _, scope := range scopes {
		if strings.EqualFold(scope, expected) {
			return true
		}
	}
	return false
}

func githubPublishVerificationURLAllowed(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Host, "github.com") && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func githubPublishUncertainKey(sessionID, root, owner, name string) string {
	return strings.ToLower(strings.TrimSpace(sessionID) + "\x00" + filepath.Clean(root) + "\x00" + owner + "\x00" + name)
}

func githubPublishHTMLURL(owner, name string) string {
	return "https://github.com/" + owner + "/" + name
}

func githubPublishCloneURL(owner, name string) string {
	return githubPublishHTMLURL(owner, name) + ".git"
}

func githubPublishRepoURL(raw string, owner, name string, clone bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GitHub returned an untrusted repository URL")
	}
	pathValue := strings.Trim(parsed.Path, "/")
	if clone {
		pathValue = strings.TrimSuffix(pathValue, ".git")
	}
	expected := owner + "/" + name
	if !strings.EqualFold(pathValue, expected) {
		return "", errors.New("GitHub returned a repository URL for a different identity")
	}
	if clone {
		return githubPublishCloneURL(owner, name), nil
	}
	return githubPublishHTMLURL(owner, name), nil
}

func githubPublishValidateRepository(repo *githubPublishRepo, owner, name, expectedVisibility string) (*githubPublishRepo, error) {
	if repo == nil || repo.ID <= 0 || strings.TrimSpace(repo.OwnerLogin) == "" || strings.TrimSpace(repo.Name) == "" || strings.TrimSpace(repo.FullName) == "" || !strings.EqualFold(repo.OwnerLogin, owner) || !strings.EqualFold(repo.Name, name) || !strings.EqualFold(repo.FullName, owner+"/"+name) {
		return nil, errors.New("GitHub response did not match the requested repository identity")
	}
	if expectedVisibility != "" && ((expectedVisibility == "private") != repo.Private) {
		return nil, errors.New("GitHub response did not preserve the confirmed repository visibility")
	}
	htmlURL, err := githubPublishRepoURL(repo.HTMLURL, owner, name, false)
	if err != nil {
		return nil, err
	}
	cloneURL, err := githubPublishRepoURL(repo.CloneURL, owner, name, true)
	if err != nil {
		return nil, err
	}
	repo.HTMLURL = htmlURL
	repo.CloneURL = cloneURL
	return repo, nil
}

func githubPublishRepoURLDisplay(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "<redacted remote URL>"
	}
	return githubPublishSafeText(strings.TrimSpace(raw))
}

func githubPublishRemoteModel(remote githubPublishRemote, owner, name string) GitHubPublishRemote {
	return GitHubPublishRemote{Name: remote.name, FetchURL: githubPublishRepoURLDisplay(remote.fetchURL), PushURL: githubPublishRepoURLDisplay(remote.pushURL), MatchesTarget: owner != "" && githubPublishRemoteMatchesTarget(remote, owner, name)}
}

func githubPublishTargetModel(repo *githubPublishRepo, owner, name string) GitHubPublishTarget {
	return GitHubPublishTarget{Owner: owner, Name: name, URL: repo.HTMLURL, CloneURL: repo.CloneURL, RepositoryID: repo.ID, State: "existing_accessible", Exists: true, AccessKnown: true, Accessible: true, CanPush: repo.CanPush, Private: repo.Private, Description: repo.Description}
}

func githubPublishRemoteTarget(raw string) (string, string, bool, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false, false
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		if !strings.EqualFold(parsed.Hostname(), "github.com") {
			return "", "", false, parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
		}
		pathValue := strings.Trim(parsed.Path, "/")
		pathValue = strings.TrimSuffix(pathValue, ".git")
		parts := strings.Split(pathValue, "/")
		if len(parts) != 2 {
			return "", "", false, parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
		}
		return parts[0], parts[1], true, parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
	}
	if strings.HasPrefix(raw, "git@github.com:") {
		pathValue := strings.TrimSuffix(strings.Trim(strings.TrimPrefix(raw, "git@github.com:"), "/"), ".git")
		parts := strings.Split(pathValue, "/")
		if len(parts) == 2 {
			return parts[0], parts[1], true, false
		}
	}
	return "", "", false, false
}

func githubPublishRemoteMatchesTarget(remote githubPublishRemote, owner, name string) bool {
	fetchOwner, fetchName, fetchOK, _ := githubPublishRemoteTarget(remote.fetchURL)
	pushOwner, pushName, pushOK, _ := githubPublishRemoteTarget(remote.pushURL)
	if remote.fetchURL != "" && !fetchOK {
		return false
	}
	if remote.pushURL != "" && !pushOK {
		return false
	}
	if !fetchOK && !pushOK {
		return false
	}
	if fetchOK && (!strings.EqualFold(fetchOwner, owner) || !strings.EqualFold(fetchName, name)) {
		return false
	}
	if pushOK && (!strings.EqualFold(pushOwner, owner) || !strings.EqualFold(pushName, name)) {
		return false
	}
	return true
}

func githubPublishRemoteURLsSafe(remote *githubPublishRemote) bool {
	if remote == nil {
		return false
	}
	for _, value := range []string{remote.fetchURL, remote.pushURL} {
		if value == "" {
			continue
		}
		if _, _, _, unsafe := githubPublishRemoteTarget(value); unsafe {
			return false
		}
		parsed, err := url.Parse(value)
		if err == nil && (parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
			return false
		}
	}
	return true
}

func githubPublishFindRemote(remotes []githubPublishRemote, name string) *githubPublishRemote {
	for index := range remotes {
		if remotes[index].name == name {
			copy := remotes[index]
			return &copy
		}
	}
	return nil
}

func githubPublishChooseRemote(remotes []githubPublishRemote, owner, name string) (string, *githubPublishRemote) {
	origin := githubPublishFindRemote(remotes, "origin")
	if origin == nil {
		return "origin", nil
	}
	if githubPublishRemoteMatchesTarget(*origin, owner, name) {
		return "origin", origin
	}
	for index := 0; ; index++ {
		candidate := "github"
		if index > 0 {
			candidate = "github-" + strconv.Itoa(index+1)
		}
		remote := githubPublishFindRemote(remotes, candidate)
		if remote == nil {
			return candidate, nil
		}
		if githubPublishRemoteMatchesTarget(*remote, owner, name) {
			return candidate, remote
		}
	}
}

func parseGitHubDeviceResponse(body []byte) (githubDeviceResponse, error) {
	var response githubDeviceResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return response, errors.New("GitHub device authorization response was not valid JSON")
	}
	return response, nil
}

type githubDeviceResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type githubPublishTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	ErrorCode        string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ExpiresIn        int    `json:"expires_in"`
	Interval         int    `json:"interval"`
}

func parseGitHubTokenResponse(body []byte) (githubPublishTokenResponse, error) {
	var response githubPublishTokenResponse
	if err := json.Unmarshal(body, &response); err == nil {
		return response, nil
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return response, errors.New("GitHub authorization response was not valid JSON")
	}
	response.AccessToken = values.Get("access_token")
	response.TokenType = values.Get("token_type")
	response.Scope = values.Get("scope")
	response.ErrorCode = values.Get("error")
	response.ErrorDescription = values.Get("error_description")
	return response, nil
}

func parseGitHubRepository(body []byte) (*githubPublishRepo, error) {
	var response struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		FullName    string  `json:"full_name"`
		Description *string `json:"description"`
		Private     bool    `json:"private"`
		HTMLURL     string  `json:"html_url"`
		CloneURL    string  `json:"clone_url"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("GitHub repository response was not valid JSON")
	}
	description := ""
	if response.Description != nil {
		description = *response.Description
	}
	return &githubPublishRepo{ID: response.ID, Name: response.Name, FullName: response.FullName, OwnerLogin: response.Owner.Login, Description: description, Private: response.Private, HTMLURL: response.HTMLURL, CloneURL: response.CloneURL, CanPush: response.Permissions.Push}, nil
}

func (service *githubPublishService) doHTTP(ctx context.Context, method, endpoint string, body []byte, accessToken string, api bool) (int, http.Header, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if api {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", githubPublishAPIVersion)
		if accessToken != "" {
			request.Header.Set("Authorization", "Bearer "+accessToken)
		}
	}
	if body != nil {
		if api {
			request.Header.Set("Content-Type", "application/json")
		} else {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	response, err := service.http.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	if response == nil {
		return 0, nil, nil, errors.New("GitHub returned no HTTP response")
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, githubPublishMaxBodyBytes+1))
	if readErr != nil {
		return response.StatusCode, response.Header, nil, readErr
	}
	if len(responseBody) > githubPublishMaxBodyBytes {
		return response.StatusCode, response.Header, nil, errors.New("GitHub response exceeded the safe size limit")
	}
	return response.StatusCode, response.Header, responseBody, nil
}

func githubPublishHTTPFailure(status int, body []byte) error {
	code, message := githubPublishErrorFields(body)
	if message == "" {
		message = http.StatusText(status)
	}
	if code != "" {
		message = code + ": " + message
	}
	return &githubPublishHTTPError{status: status, message: githubPublishSafeText(message)}
}

type githubPublishHTTPError struct {
	status  int
	message string
}

func (failure *githubPublishHTTPError) Error() string {
	if failure == nil {
		return "GitHub request failed"
	}
	return fmt.Sprintf("GitHub request failed (%d): %s", failure.status, failure.message)
}

func githubPublishErrorFields(body []byte) (string, string) {
	var response struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
		Errors           []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &response) == nil {
		message := response.ErrorDescription
		if message == "" {
			message = response.Message
		}
		if message == "" {
			message = response.Error
		}
		if message == "" && len(response.Errors) > 0 {
			message = response.Errors[0].Message
		}
		code := response.Error
		if code == "" && len(response.Errors) > 0 {
			code = response.Errors[0].Code
		}
		return githubPublishSafeText(code), githubPublishSafeText(message)
	}
	return "", ""
}

func githubPublishSafeText(value string) string {
	value = redactGitSecrets(value)
	value = githubPublishAuthorizationPattern.ReplaceAllString(value, "$1<redacted>")
	value = githubPublishTokenPattern.ReplaceAllString(value, "<redacted>")
	value = strings.TrimSpace(value)
	if len(value) > githubPublishMaxErrorLength {
		value = value[:githubPublishMaxErrorLength]
	}
	return value
}

func githubPublishSafeError(err error) string {
	if err == nil {
		return ""
	}
	return githubPublishSafeText(err.Error())
}

func githubPublishActionForError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(githubPublishSafeError(err))
	switch {
	case strings.Contains(message, "credential") || strings.Contains(message, "authentication"):
		return "Sign in with Git Credential Manager or the configured OS credential helper, then retry the conventional push."
	case strings.Contains(message, "non-fast-forward") || strings.Contains(message, "different commit"):
		return "Fetch and merge the remote main branch manually; never force-push from Publish to GitHub."
	case strings.Contains(message, "remote"):
		return "Review the existing remotes and choose a non-conflicting GitHub remote name; no existing remote was retargeted."
	case strings.Contains(message, "network") || strings.Contains(message, "timeout") || strings.Contains(message, "unknown"):
		return "Check the network, reconcile GitHub state, and retry only after rereading the target."
	default:
		return "Review the incomplete step and retry only after GitHub and local Git state have been reread."
	}
}

func githubPublishTargetAction(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "Authenticate again with GitHub."
	case http.StatusForbidden:
		return "Check GitHub permissions, organization policy, or rate limits."
	case http.StatusMovedPermanently:
		return "The requested repository moved; run preflight again and confirm the exact target."
	case http.StatusNotFound:
		return "GitHub did not confirm access; a 404 can mean absent or inaccessible."
	case http.StatusTooManyRequests:
		return "Wait for GitHub's rate-limit interval, then retry preflight."
	default:
		return "Check GitHub network/API status, then retry preflight."
	}
}
