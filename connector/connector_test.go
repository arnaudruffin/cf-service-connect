package connector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloud-gov/cf-service-connect/api"
)

// testAppSSHFingerprint is a deliberately fake SSH host key fingerprint. It is
// 43 characters long to match the unpadded base64 SHA-256 format that current CF
// foundations advertise, but nothing in this package verifies it against a key --
// it is only carried from the root document through to launcher.SSHTarget.
const testAppSSHFingerprint = "test-app-ssh-host-key-fingerprint-for-tests"

const (
	testServiceInstanceGUID = "6b48cfb2-67d3-4b5d-b831-f2cfce77f0dc"
	testPlanGUID            = "0480cc5c-ca73-406d-90bf-f6f9bb1b0890"
	testOfferingGUID        = "a830ff8a-c956-484c-81ce-1f3625024981"
	testKeyGUID             = "3d1e1f00-0000-4000-8000-000000000001"
	testAppGUID             = "6f5deda9-4832-4dc8-bdaa-daa45f4f6b36"
	// Deliberately different from testAppGUID: the SSH username must use the
	// process GUID, and equal GUIDs would hide a regression.
	testProcessGUID = "11112222-3333-4444-5555-666677778888"
)

// fakeFoundation serves the CAPI v3 and UAA routes that a full connect flow
// touches, and answers /v2/* the way a v2-disabled foundation does.
type fakeFoundation struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []string

	appState string

	// instanceState is the CAPI state reported for web instance 0.
	instanceState string

	// sshEnabled and sshDisabledReason mirror GET /v3/apps/:guid/ssh_enabled.
	sshEnabled        bool
	sshDisabledReason string

	// existingKeyGUID, when set, makes the credential-binding list report a
	// leftover key from a previous run.
	existingKeyGUID string

	// deleteStatus lets a test make key deletion fail.
	deleteStatus int

	// omitAppSSH removes the app_ssh link from the root document.
	omitAppSSH bool

	deletes int
}

func newFakeFoundation(t *testing.T) *fakeFoundation {
	t.Helper()

	f := &fakeFoundation{
		appState:      "STARTED",
		instanceState: "RUNNING",
		sshEnabled:    true,
		deleteStatus:  http.StatusNoContent,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeFoundation) URL() string { return f.server.URL }

func (f *fakeFoundation) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeFoundation) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

func (f *fakeFoundation) route(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	// Match capi-release's nginx behaviour when cc.temporary_enable_v2 is false:
	// a plain-text 404, not a JSON error body.
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("V2 endpoints disabled"))
		return
	}

	switch {
	case r.URL.Path == "/":
		f.writeRoot(w)
	case r.URL.Path == "/oauth/token":
		writeJSON(w, http.StatusOK, map[string]any{
			"token_type":   "bearer",
			"access_token": strings.TrimPrefix(testToken(time.Hour), "bearer "),
			"expires_in":   3600,
		})
	case r.URL.Path == "/oauth/authorize":
		w.Header().Set("Location", "https://uaa.example.com/login?code=test-ssh-code")
		w.WriteHeader(http.StatusFound)
	case r.URL.Path == "/v3/service_instances":
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{
			"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
			"resources": [{"guid": %q, "name": "my-test-service", "type": "managed",
				"relationships": {"space": {"data": {"guid": "space-guid"}}, "service_plan": {"data": {"guid": %q}}}}]
		}`, testServiceInstanceGUID, testPlanGUID))
	case r.URL.Path == "/v3/service_plans/"+testPlanGUID:
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"guid": %q, "name": "micro-psql",
			"relationships": {"service_offering": {"data": {"guid": %q}}}}`, testPlanGUID, testOfferingGUID))
	case r.URL.Path == "/v3/service_offerings/"+testOfferingGUID:
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"guid": %q, "name": "aws-rds"}`, testOfferingGUID))
	case r.URL.Path == "/v3/apps":
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{
			"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
			"resources": [{"guid": %q, "name": "test-app", "state": %q}]
		}`, testAppGUID, f.appState))
	case r.URL.Path == "/v3/apps/"+testAppGUID+"/ssh_enabled":
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"enabled": %t, "reason": %q}`, f.sshEnabled, f.sshDisabledReason))
	case r.URL.Path == "/v3/apps/"+testAppGUID+"/processes":
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{
			"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
			"resources": [{"guid": %q, "type": "web", "instances": 1}]
		}`, testProcessGUID))
	case r.URL.Path == "/v3/processes/"+testProcessGUID+"/stats":
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"resources": [{"type": "web", "index": 0, "state": %q, "details": null}]}`, f.instanceState))
	case r.URL.Path == "/v3/service_credential_bindings" && r.Method == http.MethodGet:
		f.writeBindingList(w)
	case r.URL.Path == "/v3/service_credential_bindings" && r.Method == http.MethodPost:
		writeRaw(w, http.StatusCreated, fmt.Sprintf(`{"guid": %q, "name": "SERVICE_CONNECT", "type": "key"}`, testKeyGUID))
	case strings.HasSuffix(r.URL.Path, "/details"):
		writeRaw(w, http.StatusOK, `{"credentials": {
			"host": "db.example.com", "port": 5432,
			"db_name": "testdb", "username": "testuser", "password": "testpass"}}`)
	case strings.HasPrefix(r.URL.Path, "/v3/service_credential_bindings/") && r.Method == http.MethodDelete:
		f.mu.Lock()
		f.deletes++
		f.mu.Unlock()
		if f.deleteStatus >= 400 {
			writeRaw(w, f.deleteStatus, `{"errors":[{"detail":"Cannot delete","title":"CF-UnprocessableEntity","code":10008}]}`)
			return
		}
		w.WriteHeader(f.deleteStatus)
	default:
		writeRaw(w, http.StatusNotFound, `{"errors":[{"detail":"Unknown request","title":"CF-NotFound","code":10000}]}`)
	}
}

func (f *fakeFoundation) writeBindingList(w http.ResponseWriter) {
	f.mu.Lock()
	existing := f.existingKeyGUID
	deleted := f.deletes > 0
	f.mu.Unlock()

	// Before any delete, report the leftover key if one was configured.
	// Afterwards, report the key the plugin just created.
	guid := testKeyGUID
	if existing != "" && !deleted {
		guid = existing
	}

	writeRaw(w, http.StatusOK, fmt.Sprintf(`{
		"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
		"resources": [{"guid": %q, "name": "SERVICE_CONNECT", "type": "key"}]
	}`, guid))
}

func (f *fakeFoundation) writeRoot(w http.ResponseWriter) {
	links := map[string]any{
		"self":                map[string]any{"href": f.URL()},
		"login":               map[string]any{"href": f.URL()},
		"uaa":                 map[string]any{"href": f.URL()},
		"cloud_controller_v3": map[string]any{"href": f.URL() + "/v3", "meta": map[string]any{"version": "3.229.0"}},
	}
	if !f.omitAppSSH {
		links["app_ssh"] = map[string]any{
			"href": "ssh.example.com:2222",
			"meta": map[string]any{
				"host_key_fingerprint": testAppSSHFingerprint,
				"oauth_client":         "ssh-proxy",
			},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func testToken(validFor time.Duration) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"exp":%d,"iat":%d}`, time.Now().Add(validFor).Unix(), time.Now().Unix())))
	return "bearer " + header + "." + payload + ".signature"
}

type stubConnection struct {
	apiEndpoint string
	spaceGUID   string
}

func (c stubConnection) ApiEndpoint() (string, error) { return c.apiEndpoint, nil }
func (c stubConnection) AccessToken() (string, error) { return testToken(time.Hour), nil }
func (c stubConnection) IsSSLDisabled() (bool, error) { return false, nil }
func (c stubConnection) GetCurrentSpace() (api.Space, error) {
	return api.Space{Guid: c.spaceGUID, Name: "test-space"}, nil
}

func newStubConnection(f *fakeFoundation) api.Connection {
	return stubConnection{apiEndpoint: f.URL(), spaceGUID: "space-guid"}
}

// The connect flow reaches the SSH dial, which cannot succeed against the fake
// foundation (ssh.example.com does not resolve). That is the expected stopping
// point for these tests: everything up to and including credential retrieval is
// exercised, and the assertions are about the CAPI interactions.
func TestConnectPerformsTheFullV3FlowAndCleansUp(t *testing.T) {
	f := newFakeFoundation(t)

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
		ConnectClient:       false,
	})

	// The SSH dial is expected to fail here.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSH proxy")

	paths := f.requestedPaths()
	joined := strings.Join(paths, "\n")

	// The whole point: never call v2.
	for _, path := range paths {
		assert.NotContains(t, path, "/v2/", "the plugin must not call CAPI v2")
	}

	assert.Contains(t, joined, "GET /v3/service_instances")
	assert.Contains(t, joined, "GET /v3/apps")
	assert.Contains(t, joined, "GET /v3/apps/"+testAppGUID+"/processes")
	assert.Contains(t, joined, "GET /v3/processes/"+testProcessGUID+"/stats")
	assert.Contains(t, joined, "POST /v3/service_credential_bindings")
	assert.Contains(t, joined, "/details")

	// The temporary service key must be cleaned up even though the run failed.
	assert.Positive(t, f.deleteCount(), "the temporary service key must be deleted on failure")
}

// A stopped app has no container to tunnel through. Failing early with a clear
// message beats a confusing SSH error.
func TestConnectRejectsAStoppedApp(t *testing.T) {
	f := newFakeFoundation(t)
	f.appState = "STOPPED"

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not started")

	// No service key should have been created for a run that cannot succeed.
	assert.NotContains(t, strings.Join(f.requestedPaths(), "\n"), "POST /v3/service_credential_bindings")
}

// A key left behind by an interrupted run must be removed before creating a new
// one, otherwise the create fails with "already exists".
func TestConnectRemovesALeftoverServiceKey(t *testing.T) {
	f := newFakeFoundation(t)
	f.existingKeyGUID = "leftover-key-guid"

	_ = connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	joined := strings.Join(f.requestedPaths(), "\n")
	assert.Contains(t, joined, "DELETE /v3/service_credential_bindings/leftover-key-guid",
		"a leftover key must be deleted before a new one is created")
}

// Previously the pre-emptive delete's error was discarded, so a key that could
// not be removed produced a confusing "already exists" failure from the create.
func TestConnectReportsAFailedLeftoverKeyDeletion(t *testing.T) {
	f := newFakeFoundation(t)
	f.existingKeyGUID = "leftover-key-guid"
	f.deleteStatus = http.StatusUnprocessableEntity

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not remove the existing service key")
}

// SSH prerequisites are resolved before a service key is created, so a
// foundation without an SSH endpoint fails without provisioning credentials it
// would immediately throw away.
func TestConnectFailsWhenNoSSHEndpointIsAdvertised(t *testing.T) {
	f := newFakeFoundation(t)
	f.omitAppSSH = true

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "advertises no SSH endpoint")

	assert.NotContains(t, strings.Join(f.requestedPaths(), "\n"), "POST /v3/service_credential_bindings",
		"SSH prerequisites must be checked before a service key is created")
}

// An app scaled to zero instances reports STARTED but has no container to tunnel
// through. This is the failure users hit as an opaque SSH "unable to
// authenticate" error before the instance check existed.
func TestConnectRejectsAnAppWithNoRunningInstance(t *testing.T) {
	f := newFakeFoundation(t)
	f.instanceState = "CRASHED"

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not RUNNING")

	assert.NotContains(t, strings.Join(f.requestedPaths(), "\n"), "POST /v3/service_credential_bindings",
		"a service key must not be created for an app that cannot be reached")
}

func TestConnectFailsWhenTheServiceInstanceIsMissing(t *testing.T) {
	f := newFakeFoundation(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/service_instances" {
			writeRaw(w, http.StatusOK, `{"pagination":{"total_results":0,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},"resources":[]}`)
			return
		}
		f.route(w, r)
	}))
	defer server.Close()

	err := connect(context.Background(), stubConnection{apiEndpoint: server.URL, spaceGUID: "space-guid"}, Options{
		AppName:             "test-app",
		ServiceInstanceName: "nope",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in the targeted space")
}

// End-to-end guard on the SSH username: the tunnel must be told
// "cf:<process-guid>/0", not "cf:<app-guid>/0". The fake foundation reports a
// process GUID that differs from the app GUID, so an app-GUID regression fails
// here rather than silently working on simple single-process apps.
func TestConnectTargetsTheProcessGUIDForSSH(t *testing.T) {
	f := newFakeFoundation(t)

	client, err := api.NewClient(newStubConnection(f))
	require.NoError(t, err)

	app, err := client.GetApp(context.Background(), "test-app")
	require.NoError(t, err)

	target, err := sshTarget(context.Background(), client, app)
	require.NoError(t, err)

	assert.Equal(t, "cf:"+testProcessGUID+"/0", target.User)
	assert.NotEqual(t, "cf:"+testAppGUID+"/0", target.User)
}

// SSH being disabled for the app is knowable before any credentials are
// provisioned, so it must fail without creating a service key.
func TestConnectRejectsAnAppWithSSHDisabled(t *testing.T) {
	f := newFakeFoundation(t)
	f.sshEnabled = false
	f.sshDisabledReason = "ssh is disabled for app"

	err := connect(context.Background(), newStubConnection(f), Options{
		AppName:             "test-app",
		ServiceInstanceName: "my-test-service",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh is disabled for app")
	assert.Contains(t, err.Error(), "cf enable-ssh test-app")

	assert.NotContains(t, strings.Join(f.requestedPaths(), "\n"), "POST /v3/service_credential_bindings",
		"a service key must not be created when SSH is disabled")
}
