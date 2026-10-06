package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	thumbnailMaxEdge     = 256
	thumbnailJPEGQuality = 82
)

var thumbnailLocks sync.Map

// ensureAssetThumbnail creates the small derivative beside the cached source.
// Decoding failures are returned so callers can keep serving the source asset;
// the cache handler deliberately treats them as a non-fatal fallback.
func ensureAssetThumbnail(asset AssetRecord) (string, error) {
	sha := strings.ToLower(strings.TrimSpace(asset.SHA256))
	if !assetIDPattern.MatchString(sha) {
		return "", errors.New("asset SHA-256 is invalid")
	}
	sourcePath := filepath.Clean(asset.Path)
	if sourcePath == "." || strings.TrimSpace(asset.Path) == "" {
		return "", errors.New("asset path is empty")
	}
	derivativePath := filepath.Join(filepath.Dir(sourcePath), sha+".thumb.jpg")
	lockValue, _ := thumbnailLocks.LoadOrStore(sha, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	file, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	config, format, err := image.DecodeConfig(file)
	_ = file.Close()
	if err != nil {
		return "", err
	}
	if format != "png" && format != "jpeg" {
		return "", fmt.Errorf("unsupported thumbnail source format %q", format)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return "", errors.New("thumbnail source has invalid dimensions")
	}
	if config.Width <= thumbnailMaxEdge && config.Height <= thumbnailMaxEdge {
		return sourcePath, nil
	}
	if info, statErr := os.Stat(derivativePath); statErr == nil {
		if info.Mode().IsRegular() && info.Size() > 0 {
			return derivativePath, nil
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}

	file, err = os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	decoded, decodedFormat, err := image.Decode(file)
	_ = file.Close()
	if err != nil {
		return "", err
	}
	if decodedFormat != "png" && decodedFormat != "jpeg" {
		return "", fmt.Errorf("unsupported thumbnail source format %q", decodedFormat)
	}
	if decoded.Bounds().Dx() <= 0 || decoded.Bounds().Dy() <= 0 {
		return "", errors.New("thumbnail source has invalid dimensions")
	}
	thumbnail := downscaleThumbnail(decoded, thumbnailMaxEdge)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, thumbnail, &jpeg.Options{Quality: thumbnailJPEGQuality}); err != nil {
		return "", err
	}
	if err := writeFileAtomic(derivativePath, encoded.Bytes(), 0o644); err != nil {
		// Another process may have won the race to create the destination. An
		// atomic rename means that winner's complete file is safe to use.
		if info, statErr := os.Stat(derivativePath); statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return derivativePath, nil
		}
		return "", err
	}
	return derivativePath, nil
}

// backfillAssetThumbnails generates the derivatives an existing library never
// had. Without it the first scroll through a library indexed before this
// existed pays the full decode once per image, which is exactly the stutter
// the derivatives were added to remove.
//
// It is deliberately unhurried: two workers, oldest-largest first, and every
// failure is skipped rather than retried, so a corrupt image cannot spin.
func backfillAssetThumbnails(ctx context.Context, store *Store) {
	if store == nil {
		return
	}
	assets, err := store.AssetsNeedingThumbnails(ctx)
	if err != nil || len(assets) == 0 {
		return
	}
	queue := make(chan AssetRecord)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for asset := range queue {
				if ctx.Err() != nil {
					return
				}
				_, _ = ensureAssetThumbnail(asset)
			}
		}()
	}
	for _, asset := range assets {
		select {
		case <-ctx.Done():
			close(queue)
			workers.Wait()
			return
		case queue <- asset:
		}
	}
	close(queue)
	workers.Wait()
}

func downscaleThumbnail(source image.Image, longestEdge int) image.Image {
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth <= longestEdge && sourceHeight <= longestEdge {
		return source
	}
	longestEdge = max(1, longestEdge)
	destinationWidth, destinationHeight := sourceWidth, sourceHeight
	if sourceWidth >= sourceHeight {
		destinationWidth = longestEdge
		destinationHeight = max(1, (sourceHeight*longestEdge+sourceWidth/2)/sourceWidth)
	} else {
		destinationHeight = longestEdge
		destinationWidth = max(1, (sourceWidth*longestEdge+sourceHeight/2)/sourceHeight)
	}
	destination := image.NewRGBA(image.Rect(0, 0, destinationWidth, destinationHeight))
	const (
		backgroundR uint64 = 11 * 257
		backgroundG uint64 = 14 * 257
		backgroundB uint64 = 20 * 257
	)
	for destinationY := range destinationHeight {
		sourceY0 := bounds.Min.Y + destinationY*sourceHeight/destinationHeight
		sourceY1 := bounds.Min.Y + ((destinationY+1)*sourceHeight+destinationHeight-1)/destinationHeight
		if sourceY1 <= sourceY0 {
			sourceY1 = sourceY0 + 1
		}
		for destinationX := range destinationWidth {
			sourceX0 := bounds.Min.X + destinationX*sourceWidth/destinationWidth
			sourceX1 := bounds.Min.X + ((destinationX+1)*sourceWidth+destinationWidth-1)/destinationWidth
			if sourceX1 <= sourceX0 {
				sourceX1 = sourceX0 + 1
			}
			var red, green, blue uint64
			var count uint64
			for sourceY := sourceY0; sourceY < sourceY1; sourceY++ {
				for sourceX := sourceX0; sourceX < sourceX1; sourceX++ {
					r, g, b, a := source.At(sourceX, sourceY).RGBA()
					alpha := uint64(a)
					oneMinusAlpha := uint64(65535) - alpha
					red += uint64(r) + backgroundR*oneMinusAlpha/65535
					green += uint64(g) + backgroundG*oneMinusAlpha/65535
					blue += uint64(b) + backgroundB*oneMinusAlpha/65535
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			destination.SetRGBA(destinationX, destinationY, colorRGBAFrom16(red/count, green/count, blue/count))
		}
	}
	return destination
}

func colorRGBAFrom16(red, green, blue uint64) (value color.RGBA) {
	if red > 65535 {
		red = 65535
	}
	if green > 65535 {
		green = 65535
	}
	if blue > 65535 {
		blue = 65535
	}
	return color.RGBA{R: uint8((red + 128) / 257), G: uint8((green + 128) / 257), B: uint8((blue + 128) / 257), A: 255}
}
