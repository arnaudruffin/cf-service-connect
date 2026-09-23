package service

import (
	"strconv"
	"testing"

	"github.com/cloud-gov/cf-service-connect/models"
	"github.com/stretchr/testify/assert"
)

type mongoDBMatchTest struct {
	serviceName string
	planName    string
	expected    bool
}

func TestGetMongoLaunchFlagsEnablesTLSForAForwardedConnection(t *testing.T) {
	credentials := &mockCredentials{
		mockPassword: "fake-password",
		mockTLS:      true,
	}

	flags := getMongoLaunchFlags(63423, credentials)

	assert.Equal(t, []string{
		"-u", "",
		"-p", "fake-password",
		"--port", strconv.Itoa(63423),
		"--tls",
		"--tlsAllowInvalidHostnames",
		"",
	}, flags)
}

func TestMongoDBMatch(t *testing.T) {
	tests := []mongoDBMatchTest{
		{
			"mongo",
			"shared",
			true,
		},
		{
			"mongodb",
			"shared",
			true,
		},
		{
			"somedb",
			"shared-mongo",
			true,
		},
		{
			"aws",
			"rds",
			false,
		},
		{
			"psql",
			"shared",
			false,
		},
	}

	for _, test := range tests {
		serviceInstance := models.ServiceInstance{
			Service: test.serviceName,
			Plan:    test.planName,
		}
		result := MongoDB.Match(serviceInstance)
		assert.Equal(t, result, test.expected)
	}
}
