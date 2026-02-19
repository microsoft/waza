package bpe

func BytePairEncode(mergingBytes []byte, ranks *BinaryMap[int], length int) []int {
	if length <= 0 {
		return nil
	}
	if length > len(mergingBytes) {
		length = len(mergingBytes)
	}
	if length == 1 {
		if token, ok := ranks.GetRange(mergingBytes, 0, 1); ok {
			return []int{token}
		}
		return nil
	}

	indices := make([]int, length+1)
	ranksBuf := make([]int, length+1)

	minRank := maxRank
	minIndex := -1
	for i := 0; i < length-1; i++ {
		rank := maxRank
		if v, ok := ranks.GetRange(mergingBytes, i, i+2); ok {
			rank = v
		}
		if rank < minRank {
			minRank = rank
			minIndex = i
		}
		indices[i] = i
		ranksBuf[i] = rank
	}

	indices[length-1] = length - 1
	ranksBuf[length-1] = maxRank
	indices[length] = length
	ranksBuf[length] = maxRank

	maxIndex := length + 1
	getRank := func(startIndex, skip int) int {
		if startIndex+skip+2 < maxIndex {
			if rank, ok := ranks.GetRange(
				mergingBytes,
				indices[startIndex],
				indices[startIndex+skip+2],
			); ok {
				return rank
			}
		}
		return maxRank
	}

	for minRank != maxRank {
		ranksBuf[indices[minIndex]] = getRank(minIndex, 1)
		if minIndex > 0 {
			ranksBuf[indices[minIndex-1]] = getRank(minIndex-1, 1)
		}

		copy(indices[minIndex+1:maxIndex-1], indices[minIndex+2:maxIndex])
		maxIndex--

		minIndex = -1
		minRank = maxRank
		for i := 0; i < maxIndex-1; i++ {
			rank := ranksBuf[indices[i]]
			if rank < minRank {
				minRank = rank
				minIndex = i
			}
		}
	}

	out := make([]int, 0, maxIndex-1)
	for i := 0; i < maxIndex-1; i++ {
		token, ok := ranks.GetRange(
			mergingBytes,
			indices[i],
			indices[i+1],
		)
		if !ok {
			return out
		}
		out = append(out, token)
	}
	return out
}
