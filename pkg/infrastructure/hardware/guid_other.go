//go:build !windows

package hardware

func readWindowsMachineGUID() string {
	return ""
}
