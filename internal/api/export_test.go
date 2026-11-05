package api

// SetListScan lowers the chunk size and scan budget of filtered resource
// list pages until the returned function restores them.
func SetListScan(chunk, budget int) (restore func()) {
	oldChunk, oldBudget := listChunk, listScanBudget
	listChunk, listScanBudget = chunk, budget
	return func() { listChunk, listScanBudget = oldChunk, oldBudget }
}
