//go:build !windows && !linux

package idle

func readSystemMonotonicMicros() int64 {
	return readProcUptimeMicros()
}
