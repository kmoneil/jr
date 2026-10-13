package registry

import "time"

// SetLocalZone names the machine's zone for one test, and returns what puts
// the real one back. `local` is whatever the machine running the suite is in,
// and a test that read that would pass in one office and fail in another.
func SetLocalZone(loc *time.Location) (restore func()) {
	was := localZone
	localZone = func() *time.Location { return loc }
	return func() { localZone = was }
}
