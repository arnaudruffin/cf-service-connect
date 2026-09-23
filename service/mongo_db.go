package service

import (
	"strconv"

	"github.com/cloud-gov/cf-service-connect/launcher"
	"github.com/cloud-gov/cf-service-connect/models"
)

type mongoDB struct{}

func (p mongoDB) Match(si models.ServiceInstance) bool {
	return si.ContainsTerms("mongo")
}

func (p mongoDB) Launch(localPort int, creds models.Credentials) error {
	return launcher.StartShell("mongo", getMongoLaunchFlags(localPort, creds))
}

func getMongoLaunchFlags(localPort int, creds models.Credentials) []string {
	flags := []string{
		"-u", creds.GetUsername(),
		"-p", creds.GetPassword(),
		"--port", strconv.Itoa(localPort),
	}
	if creds.UsesTLS() {
		flags = append(flags, "--tls", "--tlsAllowInvalidHostnames")
	}
	return append(flags, creds.GetDBName())
}

// MongoDB is the service singleton.
var MongoDB = mongoDB{}
