package processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"time"

	imagev1 "image-processing-service/gen/image/v1"
	"image-processing-service/internal/store"
)

type Worker struct {
	db        *store.Store
	dataDir   string
	maxPixels int64
}

func New(db *store.Store, dataDir string, maxPixels int64) *Worker {
	return &Worker{db: db, dataDir: dataDir, maxPixels: maxPixels}
}
func (w *Worker) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.processOne(ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (w *Worker) processOne(ctx context.Context) error {
	j, err := w.db.Claim(ctx)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return w.db.Fail(context.Background(), j.ID, err)
	}
	input, err := os.ReadFile(j.InputPath)
	if err != nil {
		return w.db.Fail(context.Background(), j.ID, fmt.Errorf("read input: %w", err))
	}
	src, _, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return w.db.Fail(context.Background(), j.ID, fmt.Errorf("decode image: %w", err))
	}
	bounds := src.Bounds()
	if int64(bounds.Dx())*int64(bounds.Dy()) > w.maxPixels {
		return w.db.Fail(context.Background(), j.ID, errors.New("image exceeds pixel limit"))
	}
	var dst image.Image
	switch j.Operation {
	case imagev1.Operation_OPERATION_GRAYSCALE.String():
		dst = grayscale(src)
	case imagev1.Operation_OPERATION_RESIZE.String():
		dst = resizeNearest(src, int(j.TargetWidth), int(j.TargetHeight))
	case imagev1.Operation_OPERATION_THUMBNAIL.String():
		ww, hh := fit(bounds.Dx(), bounds.Dy(), 512, 512)
		dst = resizeNearest(src, ww, hh)
	default:
		return w.db.Fail(context.Background(), j.ID, errors.New("unknown operation"))
	}
	extension := ".jpg"
	if j.OutputFormat == imagev1.OutputFormat_OUTPUT_FORMAT_PNG.String() {
		extension = ".png"
	}
	name := j.ID + extension
	path := filepath.Join(w.dataDir, "results", name)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return w.db.Fail(context.Background(), j.ID, fmt.Errorf("create output: %w", err))
	}
	if extension == ".png" {
		err = png.Encode(f, dst)
	} else {
		err = encodeJPEG(f, dst)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return w.db.Fail(context.Background(), j.ID, fmt.Errorf("encode output: %w", err))
	}
	info, err := os.Stat(path)
	if err != nil {
		return w.db.Fail(context.Background(), j.ID, err)
	}
	if err = w.db.Complete(ctx, j.ID, path, name, uint32(dst.Bounds().Dx()), uint32(dst.Bounds().Dy()), info.Size()); err != nil {
		return err
	}
	return nil
}
func grayscale(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}
func fit(w, h, maxW, maxH int) (int, int) {
	ratio := min(1, min(float64(maxW)/float64(w), float64(maxH)/float64(h)))
	return max(1, int(float64(w)*ratio)), max(1, int(float64(h)*ratio))
}
func resizeNearest(src image.Image, dw, dh int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	for y := 0; y < dh; y++ {
		sy := sb.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			sx := sb.Min.X + x*sw/dw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
func encodeJPEG(f *os.File, img image.Image) error {
	b := img.Bounds()
	rgb := image.NewRGBA(b)
	draw.Draw(rgb, b, img, b.Min, draw.Src)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.RGBAModel.Convert(rgb.At(x, y)).(color.RGBA)
			if c.A < 255 {
				rgb.Set(x, y, color.RGBAModel.Convert(color.White))
			}
		}
	}
	return jpegEncode(f, rgb)
}

var jpegEncode = func(f *os.File, img image.Image) error { return jpeg.Encode(f, img, &jpeg.Options{Quality: 85}) }
