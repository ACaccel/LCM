package engine

// SetAfterStore installs f as the hook between the commit of a deploy and
// the update of the deployed plan, until the returned function runs. Tests
// using it must not run in parallel.
func SetAfterStore(f func()) (restore func()) {
	afterStore = f
	return func() { afterStore = nil }
}

// SetBeforeRunInsert installs f as the hook in Start between the
// concurrency decision and the write of the run row, until the returned
// function runs. Tests using it must not run in parallel.
func SetBeforeRunInsert(f func()) (restore func()) {
	beforeRunInsert = f
	return func() { beforeRunInsert = nil }
}

// SetPruneBatch sets the number of runs Prune deletes per transaction,
// until the returned function runs. Tests using it must not run in
// parallel.
func SetPruneBatch(n int) (restore func()) {
	old := pruneBatch
	pruneBatch = n
	return func() { pruneBatch = old }
}
