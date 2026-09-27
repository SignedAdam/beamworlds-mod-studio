package main

import (
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Match the library's case/accent-insensitive, numeric string ordering.
// Collators have mutable buffers, so callers share them through a pool.
var librarySortCollators = sync.Pool{New: func() any {
	return collate.New(language.Und, collate.Loose, collate.Numeric)
}}

func compareLibrarySortText(left, right string) int {
	comparator := librarySortCollators.Get().(*collate.Collator)
	result := comparator.CompareString(left, right)
	librarySortCollators.Put(comparator)
	return result
}
