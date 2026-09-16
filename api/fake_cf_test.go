package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeCF is an httptest-backed stand-in for a CF foundation: the API root
// document, the v3 endpoints this plugin uses, and the UAA endpoints that
// go-cfclient needs for token handling and SSH passcodes.
//
// Critically, it serves *no* /v2/* routes and answers them the way capi-release
// does when cc.temporary_enable_v2 is false: HTTP 404 with the plain-text body
// "V2 endpoints disabled". Any accidental reintroduction of a v2 dependency
// therefore fails these tests.
type fakeCF struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []string

	// handlers maps "METHOD /path" to a handler, checked before the defaults.
	handlers map[string]http.HandlerFunc

	// appSSH and appSSHWS control the SSH links in the root document.
	appSSH   *sshLinkFixture
	appSSHWS *sshLinkFixture

	// omitV2Link controls whether cloud_controller_v2 appears in the root
	// document, mirroring a foundation with v2 disabled.
	omitV2Link bool
}

type sshLinkFixture struct {
	HREF               string
	HostKeyFingerprint string
	OAuthClient        string
}

// Placeholder SSH host key fingerprints.
//
// These are deliberately not real fingerprints. They are 43 characters long so
// that they match the length of an unpadded base64 SHA-256 fingerprint -- the
// format current CF foundations advertise, and the one the length-based format
// detection in launcher.hostKeyCallback keys off -- but their content is plainly
// fake. Nothing in this package verifies a fingerprint against a key; the value
// is only carried from the root document through to launcher.SSHTarget, so a
// realistic value would buy nothing and would go stale when a foundation rotates
// its host key.
//
// launcher's tests, which do verify fingerprints, compute them from keys
// generated in-test.
const (
	testAppSSHFingerprint   = "test-app-ssh-host-key-fingerprint-for-tests"
	testAppSSHWSFingerprint = "test-app-ssh-ws-host-key-fingerprint-tests0"
)

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()

	f := &fakeCF{
		handlers: map[string]http.HandlerFunc{},
		appSSH: &sshLinkFixture{
			HREF:               "ssh.example.com:2222",
			HostKeyFingerprint: testAppSSHFingerprint,
			OAuthClient:        "ssh-proxy",
		},
		omitV2Link: true,
	}

	f.server = httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCF) URL() string {
	return f.server.URL
}

func (f *fakeCF) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
}

// requestedPaths returns every "METHOD /path" the client has issued.
func (f *fakeCF) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeCF) handle(methodAndPath string, handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[methodAndPath] = handler
}

func (f *fakeCF) route(w http.ResponseWriter, r *http.Request) {
	f.record(r)

	// Reproduce a v2-disabled foundation exactly: nginx returns a plain-text
	// 404 body, not JSON. See capi-release
	// jobs/cloud_controller_ng/templates/nginx_external_endpoints.conf.erb.
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("V2 endpoints disabled"))
		return
	}

	f.mu.Lock()
	handler, ok := f.handlers[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if ok {
		handler(w, r)
		return
	}

	switch {
	case r.URL.Path == "/":
		f.writeRoot(w)
	case r.URL.Path == "/oauth/token":
		f.writeToken(w)
	case r.URL.Path == "/oauth/authorize":
		w.Header().Set("Location", "https://uaa.example.com/login?code=test-ssh-code")
		w.WriteHeader(http.StatusFound)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"detail":"Unknown request","title":"CF-NotFound","code":10000}]}`))
	}
}

func (f *fakeCF) writeRoot(w http.ResponseWriter) {
	links := map[string]any{
		"self":  map[string]any{"href": f.URL()},
		"login": map[string]any{"href": f.URL()},
		"uaa":   map[string]any{"href": f.URL()},
		"cloud_controller_v3": map[string]any{
			"href": f.URL() + "/v3",
			"meta": map[string]any{"version": "3.229.0"},
		},
	}
	if !f.omitV2Link {
		links["cloud_controller_v2"] = map[string]any{
			"href": f.URL() + "/v2",
			"meta": map[string]any{"version": "2.294.0"},
		}
	}
	if f.appSSH != nil {
		links["app_ssh"] = f.appSSH.toJSON()
	}
	if f.appSSHWS != nil {
		links["app_ssh_ws"] = f.appSSHWS.toJSON()
	}

	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (l *sshLinkFixture) toJSON() map[string]any {
	return map[string]any{
		"href": l.HREF,
		"meta": map[string]any{
			"host_key_fingerprint": l.HostKeyFingerprint,
			"oauth_client":         l.OAuthClient,
		},
	}
}

func (f *fakeCF) writeToken(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"token_type":    "bearer",
		"access_token":  strings.TrimPrefix(testAccessToken(time.Hour), "bearer "),
		"refresh_token": "refresh",
		"expires_in":    3600,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// testAccessToken builds an unsigned JWT whose exp claim is validUntil from now.
// go-cfclient only reads the exp claim from the token, so a real signature is
// not required.
func testAccessToken(validFor time.Duration) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"exp":%d,"iat":%d,"scope":["cloud_controller.read","cloud_controller.write"]}`,
			time.Now().Add(validFor).Unix(), time.Now().Unix())))
	return "bearer " + header + "." + payload + ".signature"
}

// fakeConnection is a stub plugin connection.
type fakeConnection struct {
	apiEndpoint string
	spaceGUID   string
	spaceName   string
	sslDisabled bool

	// tokens are handed out in order; the last is reused once exhausted.
	tokens []string

	mu         sync.Mutex
	tokenCalls int

	apiEndpointErr error
	tokenErr       error
	spaceErr       error
	sslErr         error
}

func newFakeConnection(f *fakeCF) *fakeConnection {
	return &fakeConnection{
		apiEndpoint: f.URL(),
		spaceGUID:   "space-guid",
		spaceName:   "test-space",
		tokens:      []string{testAccessToken(time.Hour)},
	}
}

func (c *fakeConnection) ApiEndpoint() (string, error) {
	return c.apiEndpoint, c.apiEndpointErr
}

func (c *fakeConnection) AccessToken() (string, error) {
	if c.tokenErr != nil {
		return "", c.tokenErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	index := c.tokenCalls
	c.tokenCalls++
	if index >= len(c.tokens) {
		index = len(c.tokens) - 1
	}
	return c.tokens[index], nil
}

func (c *fakeConnection) accessTokenCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenCalls
}

func (c *fakeConnection) IsSSLDisabled() (bool, error) {
	return c.sslDisabled, c.sslErr
}

func (c *fakeConnection) GetCurrentSpace() (Space, error) {
	if c.spaceErr != nil {
		return Space{}, c.spaceErr
	}
	return Space{Guid: c.spaceGUID, Name: c.spaceName}, nil
}

func newTestClient(t *testing.T, f *fakeCF) (*Client, *fakeConnection) {
	t.Helper()

	conn := newFakeConnection(f)
	client, err := NewClient(conn)
	require.NoError(t, err)
	return client, conn
}
