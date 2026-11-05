package engine

// SetAfterStore installs f as the hook between the commit of a deploy and
// the update of the deployed plan, until the returned function runs. Tests
// using it must not run in parallel.
func SetAfterStore(f func()) (restore func()) {
	afterStore = f
	return func() { afterStore = nil }
}
