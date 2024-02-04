package util

import (
	"fmt"
	"github.com/sandwich-go/boost/version"
)

func Version() string {
	return fmt.Sprintf("%s-%s-%s", version.Version, version.Branch, version.BuildDate)
}
