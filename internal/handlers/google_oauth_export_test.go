package handlers

// SetGoogleUserInfoURLForTest points the userinfo lookup at a fake server and
// returns a func restoring the original URL.
func SetGoogleUserInfoURLForTest(u string) func() {
	prev := googleUserInfoURL
	googleUserInfoURL = u
	return func() { googleUserInfoURL = prev }
}
