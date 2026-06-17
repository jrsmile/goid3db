package search

import "container/heap"

// topK keeps the K highest-scoring hits using a bounded min-heap: the smallest
// score sits at the root so it can be evicted in O(log K) when a better hit
// arrives. This avoids sorting the entire corpus on every keystroke.
type topK struct {
	limit int
	h     minHeap
}

func newTopK(limit int) *topK {
	if limit < 1 {
		limit = 1
	}
	return &topK{limit: limit, h: make(minHeap, 0, limit)}
}

func (t *topK) push(hit Hit) {
	if len(t.h) < t.limit {
		heap.Push(&t.h, hit)
		return
	}
	if hit.Score > t.h[0].Score {
		t.h[0] = hit
		heap.Fix(&t.h, 0)
	}
}

func (t *topK) drain() []Hit {
	out := make([]Hit, len(t.h))
	copy(out, t.h)
	return out
}

type minHeap []Hit

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].Score < h[j].Score }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(Hit)) }
func (h *minHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
