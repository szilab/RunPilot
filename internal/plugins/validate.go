package plugins

import "regexp"

var idPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validID(value string) bool {
	return idPattern.MatchString(value)
}

func validEnvKey(value string) bool {
	return envKeyPattern.MatchString(value)
}
