package util

import (
	"bytes"
	"fmt"
	"github.com/sandwich-go/boost/xpanic"
	"github.com/sandwich-go/xconf"
)

func MustInitialize(valPtr interface{}, files ...string) {
	x := xconf.New(xconf.WithFiles(files...))
	xpanic.WhenError(x.Parse(valPtr))
	x.Usage()
}

func PrintYamlConfig(v interface{}, enablePrint bool) {
	if !enablePrint {
		return
	}
	bytesBuffer := bytes.NewBuffer([]byte{})
	xconf.MustSaveVarToWriter(v, xconf.ConfigTypeYAML, bytesBuffer)
	fmt.Println(bytesBuffer)
	bytesBuffer.Reset()
}
