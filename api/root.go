package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/cloud-gov/cf-service-connect/version"
)

// maxRootDocumentBytes caps how much of the root document is read, guarding
// against a hostile or misconfigured endpoint streaming without end.
const maxRootDocumentBytes = 1 << 20 // 1 MiB

// UserAgent identifies this plugin to the CF API, so that operators can
// attribute API traffic to it.
func UserAgent() string {
	return fmt.Sprintf("cf-service-connect/%s (%s; %s)", version.String(), runtime.GOOS, runtime.GOARCH)
}

// rootDocument is the subset of GET / that this plugin needs.
//
// go-cfclient's resource.Root does not model app_ssh_ws (RFC-0029 is not
// implemented upstream yet), so the root document is decoded here instead.
type rootDocument struct {
	Links struct {
		AppSSH   sshLink `json:"app_ssh"`
		AppSSHWS sshLink `json:"app_ssh_ws"`

		// CloudControllerV2 is present only on foundations that still serve the
		// v2 API. This plugin does not use v2; the field exists so that
		// diagnostics can mention it.
		CloudControllerV2 struct {
			HREF string `json:"href"`
		} `json:"cloud_controller_v2"`

		CloudControllerV3 struct {
			HREF string `json:"href"`
			Meta struct {
				Version string `json:"version"`
			} `json:"meta"`
		} `json:"cloud_controller_v3"`
	} `json:"links"`
}

type sshLink struct {
	HREF string `json:"href"`
	Meta struct {
		HostKeyFingerprint string `json:"host_key_fingerprint"`
		OAuthClient        string `json:"oauth_client"`
	} `json:"meta"`
}

// getRootDocument fetches GET / from the CF API.
//
// The root document is deliberately unauthenticated, so this uses the plain
// HTTP client rather than the authenticated one -- sending a bearer token to it
// is harmless but pointless. TLS settings are inherited from the client that
// go-cfclient built, so --skip-ssl-validation is honoured.
func (c *Client) getRootDocument(ctx context.Context) (rootDocument, error) {
	endpoint := strings.TrimSuffix(c.apiURL, "/") + "/"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return rootDocument{}, fmt.Errorf("could not build a request for the CF API root document: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", UserAgent())

	response, err := c.HTTPClient().Do(request)
	if err != nil {
		return rootDocument{}, fmt.Errorf("could not fetch the CF API root document: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return rootDocument{}, fmt.Errorf("the CF API root document returned HTTP %d", response.StatusCode)
	}

	var root rootDocument
	if err := json.NewDecoder(io.LimitReader(response.Body, maxRootDocumentBytes)).Decode(&root); err != nil {
		return rootDocument{}, fmt.Errorf("could not parse the CF API root document: %w", err)
	}

	return root, nil
}
