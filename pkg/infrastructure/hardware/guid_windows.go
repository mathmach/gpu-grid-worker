//go:build windows

package hardware

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func readWindowsMachineGUID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer func() {
		if closeErr := k.Close(); closeErr != nil {
			return
		}
	}()
	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(guid)
}
