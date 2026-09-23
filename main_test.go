package main

import (
	"fmt"
	"strings"
	"testing"

	"code.cloudfoundry.org/cli/plugin"

	"github.com/cloud-gov/cf-service-connect/api"
	"github.com/cloud-gov/cf-service-connect/connector"
	"github.com/stretchr/testify/assert"
)

type parseOptionsTest struct {
	args            string
	expectError     bool
	expectedOptions connector.Options
}

func TestParseOptions(t *testing.T) {
	tests := []parseOptionsTest{
		{
			"connect-to-service app service",
			false,
			connector.Options{
				App:           connector.ResourceReference{Name: "app"},
				Service:       connector.ResourceReference{Name: "service"},
				ConnectClient: true,
			},
		},
		{
			"connect-to-service app-space/app service-space/service",
			false,
			connector.Options{
				App: connector.ResourceReference{
					Space: "app-space",
					Name:  "app",
				},
				Service: connector.ResourceReference{
					Space: "service-space",
					Name:  "service",
				},
				ConnectClient: true,
			},
		},
		{
			"connect-to-service app-org/app-space/app service-org/service-space/service/with/slashes",
			false,
			connector.Options{
				App: connector.ResourceReference{
					Organization: "app-org",
					Space:        "app-space",
					Name:         "app",
				},
				Service: connector.ResourceReference{
					Organization: "service-org",
					Space:        "service-space",
					Name:         "service/with/slashes",
				},
				ConnectClient: true,
			},
		},
		{
			"connect-to-service -no-client app service",
			false,
			connector.Options{
				App:           connector.ResourceReference{Name: "app"},
				Service:       connector.ResourceReference{Name: "service"},
				ConnectClient: false,
			},
		},
		{
			"connect-to-service -keep-service-key app service",
			false,
			connector.Options{
				App:            connector.ResourceReference{Name: "app"},
				Service:        connector.ResourceReference{Name: "service"},
				ConnectClient:  true,
				KeepServiceKey: true,
			},
		},
		{
			"connect-to-service /app service",
			true,
			connector.Options{},
		},
		{
			"connect-to-service org//app service",
			true,
			connector.Options{},
		},
		{
			"connect-to-service org/space/ service",
			true,
			connector.Options{},
		},
		{
			"connect-to-service foo bar baz",
			true,
			connector.Options{},
		},
	}

	plugin := ServiceConnectPlugin{}
	for _, test := range tests {
		args := strings.Split(test.args, " ")
		opts, err := plugin.parseOptions(args)
		if test.expectError {
			assert.NotNil(t, err)
		} else {
			assert.Nil(t, err)
			assert.Equal(t, opts, test.expectedOptions)
		}
	}
}

// The plugin's advertised version must match the version reported in the CF API
// User-Agent, so operators can correlate the two.
func TestMetadataVersionMatchesTheAPIUserAgent(t *testing.T) {
	metadata := (&ServiceConnectPlugin{}).GetMetadata()

	advertised := fmt.Sprintf("%d.%d.%d",
		metadata.Version.Major, metadata.Version.Minor, metadata.Version.Build)

	assert.Contains(t, api.UserAgent(), "cf-service-connect/"+advertised)
}

func TestMetadataDocumentsQualifiedResourceSyntax(t *testing.T) {
	usage := (&ServiceConnectPlugin{}).GetMetadata().Commands[0].UsageDetails.Usage

	assert.Contains(t, usage, "APP|SPACE/APP|ORG/SPACE/APP")
	assert.Contains(t, usage, "SERVICE|SPACE/SERVICE|ORG/SPACE/SERVICE")
}

func TestMetadataVersionIsAtLeastV2(t *testing.T) {
	metadata := (&ServiceConnectPlugin{}).GetMetadata()

	assert.GreaterOrEqual(t, metadata.Version.Major, 2,
		"the CAPI v3-only rewrite is a major version change")
}

// MinCliVersion must stay unset. Declaring any non-zero value makes the CLI
// parse its own version as semver, which fails on the Homebrew cloudfoundry-cli
// build -- its RFC3339 build date contains colons, which are illegal in semver
// build metadata -- aborting every plugin command with "Invalid character(s)
// found in build meta data". See upstream cloudfoundry/cli#3480 and the comment
// in GetMetadata.
//
// The CF CLI v8 requirement is documented in the README instead.
func TestMetadataDeclaresNoMinCliVersion(t *testing.T) {
	metadata := (&ServiceConnectPlugin{}).GetMetadata()

	assert.Equal(t, plugin.VersionType{}, metadata.MinCliVersion,
		"declaring a MinCliVersion triggers a CF CLI semver bug on Homebrew builds; see cloudfoundry/cli#3480")

	// Guard the mechanism, not just the value: the CLI only skips its check when
	// MinCliVersionStr returns an empty string.
	assert.Empty(t, plugin.MinCliVersionStr(metadata.MinCliVersion),
		"a non-empty MinCliVersion string makes the CLI parse its own version")
}
