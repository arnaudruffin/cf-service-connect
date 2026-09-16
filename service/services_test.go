package service

import (
	"testing"

	"github.com/cloud-gov/cf-service-connect/models"
	"github.com/stretchr/testify/assert"
)

type getServiceTest struct {
	serviceName   string
	planName      string
	expectFound   bool
	expectService Service
}

func TestGetService(t *testing.T) {
	tests := []getServiceTest{
		{
			"psql",
			"shared",
			true,
			PSQL,
		},
		{
			"mysql",
			"shared",
			true,
			MySQL,
		},
		{
			"other",
			"service",
			false,
			nil,
		},
	}

	for _, test := range tests {
		serviceInstance := models.ServiceInstance{
			Service: test.serviceName,
			Plan:    test.planName,
		}
		srv, found := GetService(serviceInstance)
		assert.Equal(t, found, test.expectFound)
		if test.expectFound {
			assert.Equal(t, srv, test.expectService)
		}
	}

}

// Real-world regression guard: the aws-rds broker on cloud.gov reports the
// offering as "aws-rds" and the plan as "micro-psql", which must resolve to the
// PostgreSQL client. The offering name alone does not identify the engine.
func TestGetServiceMatchesCloudGovAWSRDSPostgres(t *testing.T) {
	instance := models.ServiceInstance{
		GUID:    "6b48cfb2-67d3-4b5d-b831-f2cfce77f0dc",
		Name:    "my-test-service",
		Service: "aws-rds",
		Plan:    "micro-psql",
	}

	srv, found := GetService(instance)

	assert.True(t, found, "aws-rds/micro-psql must resolve to a client")
	assert.Equal(t, PSQL, srv)
}

// A user-provided service instance has neither offering nor plan, so no client
// can be inferred. The caller falls back to -no-client behaviour.
func TestGetServiceFindsNothingForAUserProvidedInstance(t *testing.T) {
	_, found := GetService(models.ServiceInstance{Name: "user-provided"})

	assert.False(t, found)
}
