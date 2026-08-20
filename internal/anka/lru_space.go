package anka

// templateDownloadSize returns how many bytes still need to be downloaded after
// accounting for the local cache. Anka can report cached > size when tags or
// "latest" resolve against a partially overlapping local store; treat that as
// zero remaining download so uint64 subtraction cannot underflow.
func templateDownloadSize(size, cached uint64) uint64 {
	if cached >= size {
		return 0
	}
	return size - cached
}
