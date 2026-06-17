package search

// fuzzyScore implements an fzf-style fuzzy match of pattern against text.
// Both pattern and text are expected to be lower-cased ASCII-ish bytes
// (the corpus Haystack is pre-lowercased; callers lower the query).
//
// It returns the match score and whether all pattern bytes were found in
// order. Higher scores are better: consecutive matches, matches at word
// boundaries, and matches near the start are rewarded; gaps are penalized.
//
// The algorithm is a two-pass O(len(text)) scan modeled on fzf's
// FuzzyMatchV1: a forward pass finds the match span, a backward pass tightens
// the start, then the span is scored.

const (
	scoreMatch        = 16
	scoreGapStart     = -3
	scoreGapExtension = -1

	bonusBoundary    = scoreMatch / 2 // start of a word
	bonusConsecutive = -(scoreGapStart + scoreGapExtension)
	bonusFirstChar   = 2 // multiplier applied to the first matched char's bonus
)

type charClass uint8

const (
	classNonWord charClass = iota
	classDigit
	classLower
	classUpper
)

func classOf(b byte) charClass {
	switch {
	case b >= 'a' && b <= 'z':
		return classLower
	case b >= 'A' && b <= 'Z':
		return classUpper
	case b >= '0' && b <= '9':
		return classDigit
	default:
		return classNonWord
	}
}

// bonusAt returns the position bonus for a char given the previous char's class.
func bonusAt(prev, cur charClass) int {
	if cur == classNonWord {
		return 0
	}
	// Transition from a non-word/separator char into a word char = boundary.
	if prev == classNonWord {
		return bonusBoundary
	}
	return 0
}

func fuzzyScore(pattern []byte, text string) (int, bool) {
	M := len(pattern)
	if M == 0 {
		return 0, true
	}
	N := len(text)
	if N < M {
		return 0, false
	}

	// Forward pass: find the end index where the full pattern is consumed.
	pidx := 0
	sidx := -1
	eidx := -1
	for i := 0; i < N; i++ {
		c := text[i]
		if c == pattern[pidx] {
			if sidx < 0 {
				sidx = i
			}
			pidx++
			if pidx == M {
				eidx = i + 1
				break
			}
		}
	}
	if eidx < 0 {
		return 0, false
	}

	// Backward pass: tighten the start so the span is minimal.
	pidx = M - 1
	for i := eidx - 1; i >= sidx; i-- {
		if text[i] == pattern[pidx] {
			pidx--
			if pidx < 0 {
				sidx = i
				break
			}
		}
	}

	// Score the [sidx, eidx) span.
	score := 0
	pidx = 0
	consecutive := 0
	firstBonus := 0 // boundary bonus at the start of the current consecutive run
	inGap := false
	var prevClass charClass = classNonWord
	if sidx > 0 {
		prevClass = classOf(text[sidx-1])
	}
	for i := sidx; i < eidx; i++ {
		c := text[i]
		curClass := classOf(c)
		if c == pattern[pidx] {
			score += scoreMatch
			b := bonusAt(prevClass, curClass)
			if consecutive == 0 {
				firstBonus = b
				if pidx == 0 {
					b *= bonusFirstChar
				}
			} else {
				// Within a consecutive run, inherit the strongest boundary
				// bonus seen at the run start so exact substrings beat
				// scattered boundary matches.
				if b >= bonusBoundary && b > firstBonus {
					firstBonus = b
				}
				if firstBonus > b {
					b = firstBonus
				}
				if bonusConsecutive > b {
					b = bonusConsecutive
				}
			}
			score += b
			consecutive++
			inGap = false
			pidx++
			if pidx == M {
				break
			}
		} else {
			if inGap {
				score += scoreGapExtension
			} else {
				score += scoreGapStart
			}
			inGap = true
			consecutive = 0
			firstBonus = 0
		}
		prevClass = curClass
	}

	// Prefer earlier and tighter matches slightly.
	score -= sidx / 4
	return score, true
}
