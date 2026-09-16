package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainsTerms(t *testing.T) {
	tests := []struct {
		service  string
		plan     string
		term     string
		contains bool
	}{
		{service: "foo", plan: "bar", term: "foo", contains: true},
		{service: "foo", plan: "bar", term: "bar", contains: true},
		{service: "Foo", plan: "bar", term: "foo", contains: true},
		{service: "foo", plan: "Bar", term: "bar", contains: true},
		{service: "foo", plan: "bar", term: "baz", contains: false},
		// A user-provided service instance has neither offering nor plan.
		{service: "", plan: "", term: "psql", contains: false},
	}

	for _, test := range tests {
		si := ServiceInstance{
			Service: test.service,
			Plan:    test.plan,
		}
		assert.Equal(t, test.contains, si.ContainsTerms(test.term))
	}
}

func TestContainsTermsMatchesAnyTerm(t *testing.T) {
	si := ServiceInstance{Service: "aws-rds", Plan: "micro-psql"}

	assert.True(t, si.ContainsTerms("psql", "postgres"))
	assert.True(t, si.ContainsTerms("mysql", "rds"))
	assert.False(t, si.ContainsTerms("mongo", "redis"))
}

func TestNewServiceKey(t *testing.T) {
	instance := ServiceInstance{GUID: "instance-guid", Name: "my-db"}

	key := NewServiceKey(instance)

	assert.Equal(t, instance, key.Instance)
	assert.Equal(t, "SERVICE_CONNECT", key.Name)
	assert.Empty(t, key.GUID, "the GUID is only known once the key has been created or found")
}
