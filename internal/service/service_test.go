package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	imagev1 "image-processing-service/gen/image/v1"
	"image-processing-service/internal/store"
)

func TestCreateJobValidatesAndQueuesImage(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, dir, 1<<20, 10000)
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err = png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	resp, err := s.CreateJob(context.Background(), &imagev1.CreateJobRequest{Image: b.Bytes(), Filename: "../../unsafe.png", Operation: imagev1.Operation_OPERATION_THUMBNAIL})
	if err != nil {
		t.Fatal(err)
	}
	if resp.JobId == "" || resp.Status != imagev1.JobStatus_JOB_STATUS_QUEUED {
		t.Fatalf("unexpected response: %+v", resp)
	}
	job, err := db.Get(context.Background(), resp.JobId)
	if err != nil {
		t.Fatal(err)
	}
	if job.InputFilename != "unsafe.png" {
		t.Fatalf("unsafe filename was not normalized: %q", job.InputFilename)
	}
}

func TestCreateJobRejectsOversizeAndInvalidImage(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, dir, 16, 100)
	for _, tc := range []struct {
		name string
		data []byte
		want codes.Code
	}{{"oversize", bytes.Repeat([]byte{1}, 17), codes.ResourceExhausted}, {"invalid", []byte("no image"), codes.InvalidArgument}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateJob(context.Background(), &imagev1.CreateJobRequest{Image: tc.data, Filename: strings.Repeat("a", 1)})
			if status.Code(err) != tc.want {
				t.Fatalf("got %v, want %v (%v)", status.Code(err), tc.want, err)
			}
		})
	}
}
