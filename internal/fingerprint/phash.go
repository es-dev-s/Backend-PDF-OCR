package fingerprint

import (
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"strings"

	"github.com/corona10/goimagehash"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

func init() {
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("png", "\x89PNG\r\n\x1a\n", png.Decode, png.DecodeConfig)
	image.RegisterFormat("gif", "GIF8", gif.Decode, gif.DecodeConfig)
	image.RegisterFormat("tiff", "II*\x00", tiff.Decode, tiff.DecodeConfig)
	image.RegisterFormat("tiffbe", "MM\x00*", tiff.Decode, tiff.DecodeConfig)
	image.RegisterFormat("webp", "RIFF", webp.Decode, webp.DecodeConfig)
}

func PerceptionHashFile(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, image.ErrFormat
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return 0, image.ErrFormat
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, err
	}
	return PerceptionHashImage(img)
}

func PerceptionHashImage(img image.Image) (uint64, error) {
	h, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return 0, err
	}
	return h.GetHash(), nil
}

func IsImageName(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".tif", ".tiff"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}
