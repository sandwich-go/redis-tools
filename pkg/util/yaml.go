package util

import (
	"bytes"
	"fmt"
	"github.com/sandwich-go/boost/xpanic"
	"github.com/sandwich-go/xconf"
)

func MustInitialize(valPtr interface{}, files ...string) {
	// 仅从配置文件加载，传空 FlagArgs 禁用 xconf 对 os.Args 的解析，
	// 避免与 cobra 的命令行 flag（--pattern/--count/--db/--all）冲突
	x := xconf.New(xconf.WithFiles(files...), xconf.WithFlagArgs())
	xpanic.WhenError(x.Parse(valPtr))
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
