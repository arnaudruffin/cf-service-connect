package main

import (
	"fmt"
	"strings"
	"testing"

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
				AppName:             "app",
				ServiceInstanceName: "service",
				ConnectClient:       true,
			},
		},
		{
			"connect-to-service -no-client app service",
			false,
			connector.Options{
				AppName:             "app",
				ServiceInstanceName: "service",
				ConnectClient:       false,
			},
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

// CF CLI v6 and v7 resolve endpoints from /v2/info, so they cannot even log in
// to a foundation with CAPI v2 disabled. Requiring v8 turns that into a clear
// message from the CLI rather than a confusing failure inside the plugin.
func TestMetadataRequiresCFCLIV8(t *testing.T) {
	metadata := (&ServiceConnectPlugin{}).GetMetadata()

	assert.GreaterOrEqual(t, metadata.Version.Major, 2,
		"the CAPI v3-only rewrite is a major version change")
	assert.Equal(t, 8, metadata.MinCliVersion.Major,
		"a v3-only plugin requires CF CLI v8")
}
