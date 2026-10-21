package processor

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	imagev1 "image-processing-service/gen/image/v1"
	"image-processing-service/internal/store"
)

func TestWorkerProcessesThumbnail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inputs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "results"), 0750); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	inPath := filepath.Join(dir, "inputs", "source.png")
	if err := os.WriteFile(inPath, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Create(context.Background(), store.Job{ID: "job-1", Status: "QUEUED", InputPath: inPath, InputFilename: "source.png", Operation: imagev1.Operation_OPERATION_THUMBNAIL.String(), OutputFormat: imagev1.OutputFormat_OUTPUT_FORMAT_PNG.String()}); err != nil {
		t.Fatal(err)
	}
	w := New(db, dir, 1_000_000)
	if err = w.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := db.Get(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "SUCCEEDED" {
		t.Fatalf("status=%s error=%s", job.Status, job.Error)
	}
	if job.Width != 20 || job.Height != 10 {
		t.Fatalf("dimensions=%dx%d", job.Width, job.Height)
	}
	f, err := os.Open(job.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds() != image.Rect(0, 0, 20, 10) {
		t.Fatalf("bounds=%v", got.Bounds())
	}
}

func TestWorkerMarksCorruptInputFailed(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "results"), 0750)
	input := filepath.Join(dir, "bad.img")
	os.WriteFile(input, []byte("invalid"), 0600)
	db, err := store.Open(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Create(context.Background(), store.Job{ID: "bad", Status: "QUEUED", InputPath: input, Operation: imagev1.Operation_OPERATION_GRAYSCALE.String(), OutputFormat: imagev1.OutputFormat_OUTPUT_FORMAT_JPEG.String()}); err != nil {
		t.Fatal(err)
	}
	if err = New(db, dir, 1000).processOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := db.Get(context.Background(), "bad")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "FAILED" || job.Error == "" {
		t.Fatalf("expected failed job, got %+v", job)
	}
}
