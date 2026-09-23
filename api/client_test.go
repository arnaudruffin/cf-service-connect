package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testServiceInstanceGUID = "6b48cfb2-67d3-4b5d-b831-f2cfce77f0dc"
	testPlanGUID            = "0480cc5c-ca73-406d-90bf-f6f9bb1b0890"
	testOfferingGUID        = "a830ff8a-c956-484c-81ce-1f3625024981"
	testKeyGUID             = "3d1e1f00-0000-4000-8000-000000000001"
	testAppGUID             = "6f5deda9-4832-4dc8-bdaa-daa45f4f6b36"
	testJobGUID             = "9a9a0000-0000-4000-8000-00000000000b"
)

// The fixtures below are shaped after real responses captured from
// api.dev.us-gov-west-1.aws-us-gov.cloud.gov (CAPI 3.229.0).

func serviceInstanceListJSON(planGUID string) string {
	planRelationship := `"service_plan": null`
	if planGUID != "" {
		planRelationship = fmt.Sprintf(`"service_plan": {"data": {"guid": %q}}`, planGUID)
	}

	return fmt.Sprintf(`{
		"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
		"resources": [
			{
				"guid": %q,
				"name": "my-test-service",
				"type": "managed",
				"last_operation": {"type": "create", "state": "succeeded"},
				"relationships": {
					"space": {"data": {"guid": "space-guid"}},
					%s
				}
			}
		]
	}`, testServiceInstanceGUID, planRelationship)
}

func servicePlanJSON() string {
	return fmt.Sprintf(`{
		"guid": %q,
		"name": "micro-psql",
		"relationships": {"service_offering": {"data": {"guid": %q}}}
	}`, testPlanGUID, testOfferingGUID)
}

func serviceOfferingJSON() string {
	return fmt.Sprintf(`{"guid": %q, "name": "aws-rds"}`, testOfferingGUID)
}

func appListJSON(state string) string {
	return fmt.Sprintf(`{
		"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
		"resources": [{"guid": %q, "name": "test-app", "state": %q}]
	}`, testAppGUID, state)
}

func credentialBindingListJSON(guid string) string {
	if guid == "" {
		return `{
			"pagination": {"total_results": 0, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
			"resources": []
		}`
	}
	return fmt.Sprintf(`{
		"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
		"resources": [{"guid": %q, "name": "SERVICE_CONNECT", "type": "key"}]
	}`, guid)
}

func stubServiceInstance(f *fakeCF, planGUID string) {
	f.handle("GET /v3/service_instances", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, serviceInstanceListJSON(planGUID))
	})
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestNewClientRequiresATargetedSpace(t *testing.T) {
	f := newFakeCF(t)
	conn := newFakeConnection(f)
	conn.spaceGUID = ""

	_, err := NewClient(conn)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space is targeted")
}

func TestNewClientRequiresLogin(t *testing.T) {
	f := newFakeCF(t)
	conn := newFakeConnection(f)
	conn.tokens = []string{""}

	_, err := NewClient(conn)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
}

func TestNewClientRequiresAnAPIEndpoint(t *testing.T) {
	f := newFakeCF(t)
	conn := newFakeConnection(f)
	conn.apiEndpoint = ""

	_, err := NewClient(conn)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CF API endpoint is targeted")
}

func TestNewClientPropagatesConnectionErrors(t *testing.T) {
	f := newFakeCF(t)

	t.Run("access token", func(t *testing.T) {
		conn := newFakeConnection(f)
		conn.tokenErr = errors.New("boom")
		_, err := NewClient(conn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not obtain a CF API access token")
	})

	t.Run("space", func(t *testing.T) {
		conn := newFakeConnection(f)
		conn.spaceErr = errors.New("boom")
		_, err := NewClient(conn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not determine the targeted space")
	})

	t.Run("ssl setting", func(t *testing.T) {
		conn := newFakeConnection(f)
		conn.sslErr = errors.New("boom")
		_, err := NewClient(conn)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not determine the TLS validation setting")
	})
}

func TestResolveSpaceGUIDUsesCurrentSpaceWithoutQualifiers(t *testing.T) {
	f := newFakeCF(t)
	client, _ := newTestClient(t, f)

	guid, err := client.ResolveSpaceGUID(context.Background(), "", "")

	require.NoError(t, err)
	assert.Equal(t, "space-guid", guid)
}

func TestResolveSpaceGUIDFindsSpaceInCurrentOrganization(t *testing.T) {
	f := newFakeCF(t)
	var query string
	f.handle("GET /v3/spaces", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeRaw(w, http.StatusOK, `{
			"pagination":{"total_results":1,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},
			"resources":[{"guid":"other-space-guid","name":"other-space"}]
		}`)
	})
	client, _ := newTestClient(t, f)

	guid, err := client.ResolveSpaceGUID(context.Background(), "", "other-space")

	require.NoError(t, err)
	assert.Equal(t, "other-space-guid", guid)
	assert.Contains(t, query, "names=other-space")
	assert.Contains(t, query, "organization_guids=org-guid")
}

func TestResolveSpaceGUIDFindsSpaceInNamedOrganization(t *testing.T) {
	f := newFakeCF(t)
	var organizationQuery string
	var spaceQuery string
	f.handle("GET /v3/organizations", func(w http.ResponseWriter, r *http.Request) {
		organizationQuery = r.URL.RawQuery
		writeRaw(w, http.StatusOK, `{
			"pagination":{"total_results":1,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},
			"resources":[{"guid":"other-org-guid","name":"other-org"}]
		}`)
	})
	f.handle("GET /v3/spaces", func(w http.ResponseWriter, r *http.Request) {
		spaceQuery = r.URL.RawQuery
		writeRaw(w, http.StatusOK, `{
			"pagination":{"total_results":1,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},
			"resources":[{"guid":"other-space-guid","name":"other-space"}]
		}`)
	})
	client, _ := newTestClient(t, f)

	guid, err := client.ResolveSpaceGUID(context.Background(), "other-org", "other-space")

	require.NoError(t, err)
	assert.Equal(t, "other-space-guid", guid)
	assert.Contains(t, organizationQuery, "names=other-org")
	assert.Contains(t, spaceQuery, "names=other-space")
	assert.Contains(t, spaceQuery, "organization_guids=other-org-guid")
}

func TestResolveSpaceGUIDReportsMissingOrganization(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/organizations", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{
			"pagination":{"total_results":0,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},
			"resources":[]
		}`)
	})
	client, _ := newTestClient(t, f)

	_, err := client.ResolveSpaceGUID(context.Background(), "missing-org", "some-space")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `organization "missing-org" not found`)
}

func TestResolveSpaceGUIDReportsMissingSpaceInOrganization(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/spaces", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{
			"pagination":{"total_results":0,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},
			"resources":[]
		}`)
	})
	client, _ := newTestClient(t, f)

	_, err := client.ResolveSpaceGUID(context.Background(), "", "missing-space")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `space "missing-space" not found in organization "test-org"`)
}

func TestGetServiceInstanceResolvesPlanAndOffering(t *testing.T) {
	f := newFakeCF(t)
	stubServiceInstance(f, testPlanGUID)
	f.handle("GET /v3/service_plans/"+testPlanGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, servicePlanJSON())
	})
	f.handle("GET /v3/service_offerings/"+testOfferingGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, serviceOfferingJSON())
	})

	client, _ := newTestClient(t, f)

	instance, err := client.GetServiceInstance(context.Background(), "my-test-service")

	require.NoError(t, err)
	assert.Equal(t, testServiceInstanceGUID, instance.GUID)
	assert.Equal(t, "my-test-service", instance.Name)
	assert.Equal(t, "micro-psql", instance.Plan)
	assert.Equal(t, "aws-rds", instance.Offering)
}

// A user-provided service instance has no plan. That must not be an error: the
// tunnel still works, the caller just cannot auto-detect a client.
func TestGetServiceInstanceToleratesNoPlan(t *testing.T) {
	f := newFakeCF(t)
	stubServiceInstance(f, "")

	client, _ := newTestClient(t, f)

	instance, err := client.GetServiceInstance(context.Background(), "my-test-service")

	require.NoError(t, err)
	assert.Equal(t, testServiceInstanceGUID, instance.GUID)
	assert.Empty(t, instance.Plan)
	assert.Empty(t, instance.Offering)
}

// The plan or offering may be invisible to a user who can still see the
// instance. Degrade to no client auto-detection instead of failing.
func TestGetServiceInstanceToleratesUnreadablePlan(t *testing.T) {
	f := newFakeCF(t)
	stubServiceInstance(f, testPlanGUID)
	f.handle("GET /v3/service_plans/"+testPlanGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusForbidden, `{"errors":[{"detail":"You are not authorized","title":"CF-NotAuthorized","code":10003}]}`)
	})

	client, _ := newTestClient(t, f)

	instance, err := client.GetServiceInstance(context.Background(), "my-test-service")

	require.NoError(t, err)
	assert.Equal(t, testServiceInstanceGUID, instance.GUID)
	assert.Empty(t, instance.Plan)
}

// Regression guard: go-cfclient's ServicePlans.GetIncludeServiceOffering
// indexes included.service_offerings[0] with no bounds check, so using it here
// would panic on a response without that block. This asserts we do not.
func TestGetServiceInstanceDoesNotPanicOnMissingIncludedOffering(t *testing.T) {
	f := newFakeCF(t)
	stubServiceInstance(f, testPlanGUID)
	f.handle("GET /v3/service_plans/"+testPlanGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"guid": %q, "name": "micro-psql", "relationships": {}}`, testPlanGUID))
	})

	client, _ := newTestClient(t, f)

	assert.NotPanics(t, func() {
		instance, err := client.GetServiceInstance(context.Background(), "my-test-service")
		require.NoError(t, err)
		assert.Equal(t, "micro-psql", instance.Plan)
		assert.Empty(t, instance.Offering)
	})
}

func TestGetServiceInstanceNotFound(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/service_instances", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"pagination":{"total_results":0,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},"resources":[]}`)
	})

	client, _ := newTestClient(t, f)

	_, err := client.GetServiceInstance(context.Background(), "nope")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `service instance "nope" not found`)
}

func TestGetServiceInstanceScopesToTargetedSpace(t *testing.T) {
	f := newFakeCF(t)
	var query string
	f.handle("GET /v3/service_instances", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeRaw(w, http.StatusOK, serviceInstanceListJSON(""))
	})

	client, _ := newTestClient(t, f)
	_, err := client.GetServiceInstance(context.Background(), "my-test-service")
	require.NoError(t, err)

	assert.Contains(t, query, "space_guids=space-guid")
	assert.Contains(t, query, "names=my-test-service")
}

func TestGetServiceInstanceInSpaceUsesSpecifiedSpace(t *testing.T) {
	f := newFakeCF(t)
	var query string
	f.handle("GET /v3/service_instances", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeRaw(w, http.StatusOK, serviceInstanceListJSON(""))
	})

	client, _ := newTestClient(t, f)
	_, err := client.GetServiceInstanceInSpace(context.Background(), "my-test-service", "service-space-guid")
	require.NoError(t, err)

	assert.Contains(t, query, "space_guids=service-space-guid")
}

func TestGetApp(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, appListJSON("STARTED"))
	})

	client, _ := newTestClient(t, f)

	app, err := client.GetApp(context.Background(), "test-app")

	require.NoError(t, err)
	assert.Equal(t, testAppGUID, app.GUID)
	assert.Equal(t, "test-app", app.Name)
	assert.Equal(t, "STARTED", app.State)
}

func TestGetAppInSpaceUsesSpecifiedSpace(t *testing.T) {
	f := newFakeCF(t)
	var query string
	f.handle("GET /v3/apps", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeRaw(w, http.StatusOK, appListJSON("STARTED"))
	})

	client, _ := newTestClient(t, f)
	_, err := client.GetAppInSpace(context.Background(), "test-app", "app-space-guid")
	require.NoError(t, err)

	assert.Contains(t, query, "space_guids=app-space-guid")
}

func TestGetAppNotFound(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"pagination":{"total_results":0,"total_pages":1,"first":{"href":""},"last":{"href":""},"next":null,"previous":null},"resources":[]}`)
	})

	client, _ := newTestClient(t, f)

	_, err := client.GetApp(context.Background(), "missing-app")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `app "missing-app" not found`)
}

func TestFindServiceKey(t *testing.T) {
	f := newFakeCF(t)
	var query string
	f.handle("GET /v3/service_credential_bindings", func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		writeRaw(w, http.StatusOK, credentialBindingListJSON(testKeyGUID))
	})

	client, _ := newTestClient(t, f)

	guid, found, err := client.FindServiceKey(context.Background(), testServiceInstanceGUID, "SERVICE_CONNECT")

	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, testKeyGUID, guid)

	// Filtering on type=key is what distinguishes a service key from an app
	// binding in v3; without it we could delete an app's binding.
	assert.Contains(t, query, "type=key")
	assert.Contains(t, query, "service_instance_guids="+testServiceInstanceGUID)
	assert.Contains(t, query, "names=SERVICE_CONNECT")
}

func TestFindServiceKeyNotFound(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, credentialBindingListJSON(""))
	})

	client, _ := newTestClient(t, f)

	guid, found, err := client.FindServiceKey(context.Background(), testServiceInstanceGUID, "SERVICE_CONNECT")

	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, guid)
}

// v3 returns 202 + a job for a key create, where v2's POST /v2/service_keys was
// synchronous. Reading the credentials before that job finishes is the failure
// this test pins down.
func TestCreateServiceKeyWaitsForTheJob(t *testing.T) {
	f := newFakeCF(t)

	jobPolls := 0
	f.handle("POST /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", f.URL()+"/v3/jobs/"+testJobGUID)
		w.WriteHeader(http.StatusAccepted)
	})
	f.handle("GET /v3/jobs/"+testJobGUID, func(w http.ResponseWriter, _ *http.Request) {
		jobPolls++
		state := "PROCESSING"
		if jobPolls >= 2 {
			state = "COMPLETE"
		}
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"guid": %q, "operation": "service_binding.create", "state": %q, "errors": [], "warnings": []}`, testJobGUID, state))
	})
	f.handle("GET /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		if jobPolls < 2 {
			// If the implementation looked the key up before the job finished,
			// it would find nothing and fail here.
			writeRaw(w, http.StatusOK, credentialBindingListJSON(""))
			return
		}
		writeRaw(w, http.StatusOK, credentialBindingListJSON(testKeyGUID))
	})

	client, _ := newTestClient(t, f)
	client.setPollIntervalForTest(time.Millisecond)

	guid, err := client.CreateServiceKey(context.Background(), testServiceInstanceGUID, "SERVICE_CONNECT")

	require.NoError(t, err)
	assert.Equal(t, testKeyGUID, guid)
	assert.GreaterOrEqual(t, jobPolls, 2, "the create must poll the job until it completes")
}

func TestCreateServiceKeyHandlesASynchronousResponse(t *testing.T) {
	f := newFakeCF(t)
	f.handle("POST /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusCreated, fmt.Sprintf(`{"guid": %q, "name": "SERVICE_CONNECT", "type": "key"}`, testKeyGUID))
	})

	client, _ := newTestClient(t, f)

	guid, err := client.CreateServiceKey(context.Background(), testServiceInstanceGUID, "SERVICE_CONNECT")

	require.NoError(t, err)
	assert.Equal(t, testKeyGUID, guid)
}

func TestCreateServiceKeyReportsAFailedJob(t *testing.T) {
	f := newFakeCF(t)
	f.handle("POST /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", f.URL()+"/v3/jobs/"+testJobGUID)
		w.WriteHeader(http.StatusAccepted)
	})
	f.handle("GET /v3/jobs/"+testJobGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{
			"guid": %q,
			"operation": "service_binding.create",
			"state": "FAILED",
			"errors": [{"detail": "The broker rejected the bind request", "title": "CF-ServiceBrokerBadResponse", "code": 10001}],
			"warnings": []
		}`, testJobGUID))
	})

	client, _ := newTestClient(t, f)
	client.setPollIntervalForTest(time.Millisecond)

	_, err := client.CreateServiceKey(context.Background(), testServiceInstanceGUID, "SERVICE_CONNECT")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not created")
	assert.Contains(t, err.Error(), "The broker rejected the bind request")
}

func TestDeleteServiceKeyWaitsForTheJob(t *testing.T) {
	f := newFakeCF(t)
	jobPolled := false
	f.handle("DELETE /v3/service_credential_bindings/"+testKeyGUID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", f.URL()+"/v3/jobs/"+testJobGUID)
		w.WriteHeader(http.StatusAccepted)
	})
	f.handle("GET /v3/jobs/"+testJobGUID, func(w http.ResponseWriter, _ *http.Request) {
		jobPolled = true
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"guid": %q, "operation": "service_binding.delete", "state": "COMPLETE", "errors": [], "warnings": []}`, testJobGUID))
	})

	client, _ := newTestClient(t, f)
	client.setPollIntervalForTest(time.Millisecond)

	err := client.DeleteServiceKey(context.Background(), testKeyGUID)

	require.NoError(t, err)
	assert.True(t, jobPolled, "the delete must poll its job to completion")
}

func TestGetServiceKeyCredentials(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/service_credential_bindings/"+testKeyGUID+"/details", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{
			"credentials": {
				"host": "db.example.com",
				"port": 5432,
				"db_name": "testdb",
				"username": "testuser",
				"password": "testpass"
			}
		}`)
	})

	client, _ := newTestClient(t, f)

	creds, err := client.GetServiceKeyCredentials(context.Background(), testKeyGUID)

	require.NoError(t, err)
	assert.Equal(t, "db.example.com", creds["host"])
	assert.Equal(t, "testuser", creds["username"])
}

func TestGetServiceKeyCredentialsRejectsAnEmptyPayload(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/service_credential_bindings/"+testKeyGUID+"/details", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{}`)
	})

	client, _ := newTestClient(t, f)

	_, err := client.GetServiceKeyCredentials(context.Background(), testKeyGUID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials")
}

func TestGetSSHEndpointFromTheRootDocument(t *testing.T) {
	f := newFakeCF(t)
	client, _ := newTestClient(t, f)

	endpoint, err := client.GetSSHEndpoint(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ssh.example.com:2222", endpoint.Address)
	assert.Empty(t, endpoint.WebSocketURL)
	assert.Equal(t, testAppSSHFingerprint, endpoint.HostKeyFingerprint)
	assert.Equal(t, "ssh-proxy", endpoint.OAuthClient)
}

// RFC-0029 scenario B: both listeners advertised.
func TestGetSSHEndpointReadsBothSSHLinks(t *testing.T) {
	f := newFakeCF(t)
	f.appSSHWS = &sshLinkFixture{
		HREF:               "wss://ssh.example.com",
		HostKeyFingerprint: testAppSSHWSFingerprint,
		OAuthClient:        "ssh-proxy",
	}

	client, _ := newTestClient(t, f)

	endpoint, err := client.GetSSHEndpoint(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ssh.example.com:2222", endpoint.Address)
	assert.Equal(t, "wss://ssh.example.com", endpoint.WebSocketURL)
}

// RFC-0029 scenario C: port 2222 closed, only the WebSocket listener offered.
// The fingerprint and OAuth client must then come from app_ssh_ws.
func TestGetSSHEndpointWithOnlyTheWebSocketLink(t *testing.T) {
	f := newFakeCF(t)
	f.appSSH = nil
	f.appSSHWS = &sshLinkFixture{
		HREF:               "wss://ssh.example.com",
		HostKeyFingerprint: testAppSSHWSFingerprint,
		OAuthClient:        "ssh-proxy",
	}

	client, _ := newTestClient(t, f)

	endpoint, err := client.GetSSHEndpoint(context.Background())

	require.NoError(t, err)
	assert.Empty(t, endpoint.Address)
	assert.Equal(t, "wss://ssh.example.com", endpoint.WebSocketURL)
	assert.Equal(t, testAppSSHWSFingerprint, endpoint.HostKeyFingerprint)
	assert.Equal(t, "ssh-proxy", endpoint.OAuthClient)
}

func TestGetSSHEndpointFailsWhenNoSSHIsAdvertised(t *testing.T) {
	f := newFakeCF(t)
	f.appSSH = nil
	f.appSSHWS = nil

	client, _ := newTestClient(t, f)

	_, err := client.GetSSHEndpoint(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "advertises no SSH endpoint")
}

func TestSSHPasscode(t *testing.T) {
	f := newFakeCF(t)
	client, _ := newTestClient(t, f)

	code, err := client.SSHPasscode(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "test-ssh-code", code)
}

// The whole point of this package: no code path may touch /v2/*. The fake
// foundation answers v2 exactly as a v2-disabled CF does, so any such request
// would fail the operation and be visible in the recorded paths.
func TestNoRequestTouchesCAPIV2(t *testing.T) {
	f := newFakeCF(t)
	stubServiceInstance(f, testPlanGUID)
	f.handle("GET /v3/service_plans/"+testPlanGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, servicePlanJSON())
	})
	f.handle("GET /v3/service_offerings/"+testOfferingGUID, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, serviceOfferingJSON())
	})
	f.handle("GET /v3/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, appListJSON("STARTED"))
	})
	f.handle("POST /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusCreated, fmt.Sprintf(`{"guid": %q, "name": "SERVICE_CONNECT", "type": "key"}`, testKeyGUID))
	})
	f.handle("GET /v3/service_credential_bindings", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, credentialBindingListJSON(testKeyGUID))
	})
	f.handle("GET /v3/service_credential_bindings/"+testKeyGUID+"/details", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"credentials": {"host": "db.example.com", "port": 5432, "db_name": "d", "username": "u", "password": "p"}}`)
	})
	f.handle("DELETE /v3/service_credential_bindings/"+testKeyGUID, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestClient(t, f)
	ctx := context.Background()

	// Exercise every operation the plugin performs.
	_, err := client.GetServiceInstance(ctx, "my-test-service")
	require.NoError(t, err)
	_, err = client.GetApp(ctx, "test-app")
	require.NoError(t, err)
	_, _, err = client.FindServiceKey(ctx, testServiceInstanceGUID, "SERVICE_CONNECT")
	require.NoError(t, err)
	keyGUID, err := client.CreateServiceKey(ctx, testServiceInstanceGUID, "SERVICE_CONNECT")
	require.NoError(t, err)
	_, err = client.GetServiceKeyCredentials(ctx, keyGUID)
	require.NoError(t, err)
	_, err = client.GetSSHEndpoint(ctx)
	require.NoError(t, err)
	_, err = client.SSHPasscode(ctx)
	require.NoError(t, err)
	require.NoError(t, client.DeleteServiceKey(ctx, keyGUID))

	for _, path := range f.requestedPaths() {
		assert.False(t, strings.Contains(path, "/v2/"),
			"the plugin must never call CAPI v2, but requested %s", path)
	}
}

// A long connect-to-service session outlives a UAA access token (10 minutes on
// cloud.gov). go-cfclient is built without a refresh token, so it cannot renew
// one itself -- it fails with "token expired and refresh token is not set".
// The client must therefore ask the CF CLI for a fresh token instead.
func TestClientRefreshesAnExpiringToken(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, appListJSON("STARTED"))
	})

	conn := newFakeConnection(f)
	// The first token is already within the refresh margin; the second is not.
	conn.tokens = []string{
		testAccessToken(10 * time.Second),
		testAccessToken(time.Hour),
	}

	client, err := NewClient(conn)
	require.NoError(t, err)

	callsAfterConstruction := conn.accessTokenCalls()

	_, err = client.GetApp(context.Background(), "test-app")
	require.NoError(t, err)

	assert.Greater(t, conn.accessTokenCalls(), callsAfterConstruction,
		"a token near expiry must be replaced with a fresh one from the CF CLI")
}

func TestClientReusesAValidToken(t *testing.T) {
	f := newFakeCF(t)
	f.handle("GET /v3/apps", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, appListJSON("STARTED"))
	})

	conn := newFakeConnection(f)
	conn.tokens = []string{testAccessToken(time.Hour)}

	client, err := NewClient(conn)
	require.NoError(t, err)

	callsAfterConstruction := conn.accessTokenCalls()

	for range 3 {
		_, err = client.GetApp(context.Background(), "test-app")
		require.NoError(t, err)
	}

	assert.Equal(t, callsAfterConstruction, conn.accessTokenCalls(),
		"a token with plenty of life left must be reused")
}

func TestAccessTokenExpiry(t *testing.T) {
	t.Run("parses the exp claim", func(t *testing.T) {
		expiry, err := accessTokenExpiry(testAccessToken(30 * time.Minute))
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now().Add(30*time.Minute), expiry, 5*time.Second)
	})

	t.Run("accepts a token without the bearer prefix", func(t *testing.T) {
		token := strings.TrimPrefix(testAccessToken(time.Hour), "bearer ")
		_, err := accessTokenExpiry(token)
		assert.NoError(t, err)
	})

	t.Run("rejects a non-JWT", func(t *testing.T) {
		_, err := accessTokenExpiry("bearer not-a-jwt")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a JWT")
	})

	t.Run("rejects a token with no exp claim", func(t *testing.T) {
		_, err := accessTokenExpiry("bearer aGVhZGVy.e30.c2ln")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no exp claim")
	})
}

func TestUserAgentIdentifiesThePlugin(t *testing.T) {
	assert.Contains(t, UserAgent(), "cf-service-connect/")
}

func processListJSON(processGUID, processType string, instances int) string {
	return fmt.Sprintf(`{
		"pagination": {"total_results": 1, "total_pages": 1, "first": {"href": ""}, "last": {"href": ""}, "next": null, "previous": null},
		"resources": [{"guid": %q, "type": %q, "instances": %d}]
	}`, processGUID, processType, instances)
}

func processStatsJSON(states ...string) string {
	entries := make([]string, 0, len(states))
	for i, state := range states {
		entries = append(entries, fmt.Sprintf(`{"type": "web", "index": %d, "state": %q, "details": null}`, i, state))
	}
	return fmt.Sprintf(`{"resources": [%s]}`, strings.Join(entries, ","))
}

func stubProcesses(f *fakeCF, processGUID string, statsJSON string) {
	f.handle("GET /v3/apps/"+testAppGUID+"/processes", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, processListJSON(processGUID, "web", 1))
	})
	f.handle("GET /v3/processes/"+processGUID+"/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, statsJSON)
	})
}

func startedTestApp() App {
	return App{GUID: testAppGUID, Name: "test-app", State: "STARTED"}
}

// The Diego SSH proxy authenticates "cf:<process-guid>/<index>". For a simple
// app whose only process is "web", the process GUID equals the app GUID -- which
// is why using the app GUID appears to work. This asserts the process GUID is
// used, with a process GUID deliberately different from the app GUID.
func TestGetSSHProcessUsesTheProcessGUIDNotTheAppGUID(t *testing.T) {
	f := newFakeCF(t)
	const distinctProcessGUID = "11112222-3333-4444-5555-666677778888"
	stubProcesses(f, distinctProcessGUID, processStatsJSON("RUNNING"))

	client, _ := newTestClient(t, f)

	process, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 0)

	require.NoError(t, err)
	assert.Equal(t, distinctProcessGUID, process.GUID)
	assert.NotEqual(t, testAppGUID, process.GUID)
	assert.Equal(t, "cf:"+distinctProcessGUID+"/0", process.SSHUsername())
}

// An app scaled to zero instances still reports STARTED. Without this check the
// SSH handshake fails with an opaque "unable to authenticate" error.
func TestGetSSHProcessRejectsAnAppWithNoInstances(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID, `{"resources": []}`)

	client, _ := newTestClient(t, f)

	_, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running instances")
	assert.Contains(t, err.Error(), "cf scale test-app -i 1")
}

func TestGetSSHProcessRejectsANonRunningInstance(t *testing.T) {
	for _, state := range []string{"CRASHED", "STARTING", "DOWN"} {
		t.Run(state, func(t *testing.T) {
			f := newFakeCF(t)
			stubProcesses(f, testAppGUID, processStatsJSON(state))

			client, _ := newTestClient(t, f)

			_, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), state)
			assert.Contains(t, err.Error(), "not RUNNING")
		})
	}
}

// CAPI reports placement problems in the instance "details" field; surfacing it
// saves the user a trip to `cf app`.
func TestGetSSHProcessIncludesInstanceDetails(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID,
		`{"resources": [{"type": "web", "index": 0, "state": "CRASHED", "details": "insufficient resources: memory"}]}`)

	client, _ := newTestClient(t, f)

	_, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient resources: memory")
}

func TestGetSSHProcessRejectsAMissingInstanceIndex(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID, processStatsJSON("RUNNING"))

	client, _ := newTestClient(t, f)

	// Only index 0 exists.
	_, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 3)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestGetSSHProcessRejectsAMissingProcessType(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID, processStatsJSON("RUNNING"))

	client, _ := newTestClient(t, f)

	_, err := client.GetSSHProcess(context.Background(), startedTestApp(), "worker", 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `has no "worker" process`)
}

func TestGetSSHProcessDefaultsToTheWebProcess(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID, processStatsJSON("RUNNING"))

	client, _ := newTestClient(t, f)

	process, err := client.GetSSHProcess(context.Background(), startedTestApp(), "", 0)

	require.NoError(t, err)
	assert.Equal(t, "web", process.Type)
}

// A multi-instance app must be able to target index 0 even when later instances
// are unhealthy.
func TestGetSSHProcessAcceptsIndexZeroWhenOtherInstancesAreUnhealthy(t *testing.T) {
	f := newFakeCF(t)
	stubProcesses(f, testAppGUID, processStatsJSON("RUNNING", "CRASHED"))

	client, _ := newTestClient(t, f)

	process, err := client.GetSSHProcess(context.Background(), startedTestApp(), "web", 0)

	require.NoError(t, err)
	assert.Equal(t, 0, process.Index)
}

func stubSSHEnabled(f *fakeCF, enabled bool, reason string) {
	f.handle("GET /v3/apps/"+testAppGUID+"/ssh_enabled", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, fmt.Sprintf(`{"enabled": %t, "reason": %q}`, enabled, reason))
	})
}

func TestCheckSSHEnabledAllowsAnSSHEnabledApp(t *testing.T) {
	f := newFakeCF(t)
	stubSSHEnabled(f, true, "")

	client, _ := newTestClient(t, f)

	assert.NoError(t, client.CheckSSHEnabled(context.Background(), startedTestApp()))
}

// CAPI reports whether SSH is disabled globally, at the space level, or for the
// app. Surfacing that reason is the difference between an actionable message and
// an opaque SSH handshake failure.
func TestCheckSSHEnabledRejectsAndExplains(t *testing.T) {
	for _, reason := range []string{
		"ssh is disabled for app",
		"ssh is disabled for space",
		"ssh is disabled globally",
	} {
		t.Run(reason, func(t *testing.T) {
			f := newFakeCF(t)
			stubSSHEnabled(f, false, reason)

			client, _ := newTestClient(t, f)

			err := client.CheckSSHEnabled(context.Background(), startedTestApp())

			require.Error(t, err)
			assert.Contains(t, err.Error(), reason)
			assert.Contains(t, err.Error(), "cf enable-ssh test-app")
		})
	}
}
