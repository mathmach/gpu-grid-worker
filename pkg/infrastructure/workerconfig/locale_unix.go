//go:build !windows

package workerconfig

func detectOSLocaleName() string {
	return ""
}
