package models

import (
	"strings"
)

// ServiceInstance identifies a service instance and the offering/plan used to
// pick a matching database client.
type ServiceInstance struct {
	GUID    string
	Name    string
	Service string
	Plan    string
}

// ContainsTerms reports whether any of the given terms appears in the service
// offering or plan name, case-insensitively.
func (si *ServiceInstance) ContainsTerms(items ...string) bool {
	for _, item := range items {
		item = strings.ToLower(item)
		service := strings.ToLower(si.Service)
		plan := strings.ToLower(si.Plan)
		if strings.Contains(service, item) || strings.Contains(plan, item) {
			return true
		}
	}
	return false
}
