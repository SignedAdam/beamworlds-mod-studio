package main

import (
	"fmt"
	"math"
	"testing"
)

func TestNormalizeCollectionCoverBoundariesAndOrder(t *testing.T) {
	valid := make([]CollectionCoverImage, 9)
	for index := range valid {
		valid[index] = CollectionCoverImage{AssetID: fmt.Sprintf("%064x", index+1), FocalX: float64(index) / 8, FocalY: 1 - float64(index)/8}
	}
	cover, err := normalizeCollectionCover(CollectionCover{Mode: "collage", Images: valid})
	if err != nil {
		t.Fatalf("valid nine-image collage rejected: %v", err)
	}
	for index := range valid {
		if cover.Images[index] != valid[index] {
			t.Fatalf("collage image order/focal changed at index %d: got %#v want %#v", index, cover.Images[index], valid[index])
		}
	}
	if _, err := normalizeCollectionCover(CollectionCover{Mode: "collage", Images: append(valid, CollectionCoverImage{AssetID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FocalX: .5, FocalY: .5})}); err == nil {
		t.Fatal("tenth collage image was accepted")
	}
	if _, err := normalizeCollectionCover(CollectionCover{Mode: "collage", Images: valid[:1]}); err == nil {
		t.Fatal("one-image collage was accepted")
	}
	if _, err := normalizeCollectionCover(CollectionCover{Mode: "single", Images: valid[:2]}); err == nil {
		t.Fatal("two-image single cover was accepted")
	}
	if _, err := normalizeCollectionCover(CollectionCover{Mode: "automatic", Images: valid[:1]}); err == nil {
		t.Fatal("automatic cover with selected images was accepted")
	}
	badFocal := valid[:2]
	badFocal[0].FocalX = math.NaN()
	if _, err := normalizeCollectionCover(CollectionCover{Mode: "collage", Images: badFocal}); err == nil {
		t.Fatal("non-finite focal position was accepted")
	}
}

func TestCollectionCoverTilesFillSupportedRecipes(t *testing.T) {
	for count := 1; count <= 9; count++ {
		tiles := collectionCoverTiles(count)
		if len(tiles) != count {
			t.Fatalf("%d-image recipe produced %d tiles", count, len(tiles))
		}
		area := 0
		for index, tile := range tiles {
			if tile.width <= 0 || tile.height <= 0 {
				t.Fatalf("%d-image recipe tile %d has empty bounds: %#v", count, index, tile)
			}
			area += tile.width * tile.height
		}
		if area != collectionCoverWidth*collectionCoverHeight {
			t.Fatalf("%d-image recipe leaves gaps or overlaps: area %d want %d", count, area, collectionCoverWidth*collectionCoverHeight)
		}
	}
}
