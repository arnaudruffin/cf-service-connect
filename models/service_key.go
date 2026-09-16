package models

// ServiceKey identifies the temporary service key that the plugin creates in
// order to obtain connection credentials.
//
// In CAPI v3 a service key is a service credential binding of type "key".
type ServiceKey struct {
	Instance ServiceInstance

	// Name is the service key's name in CF.
	Name string

	// GUID is populated once the key has been created or found.
	GUID string
}

// serviceKeyName is the name used for the plugin's temporary service key.
const serviceKeyName = "SERVICE_CONNECT"

// NewServiceKey describes the service key for a given instance.
func NewServiceKey(instance ServiceInstance) ServiceKey {
	return ServiceKey{
		Instance: instance,
		Name:     serviceKeyName,
	}
}
