package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	imagev1 "image-processing-service/gen/image/v1"
	"image-processing-service/internal/store"
)

type Server struct {
	imagev1.UnimplementedImageServiceServer
	db                  *store.Store
	dataDir             string
	maxBytes, maxPixels int64
}

func New(db *store.Store, dataDir string, maxBytes, maxPixels int64) *Server {
	return &Server{db: db, dataDir: dataDir, maxBytes: maxBytes, maxPixels: maxPixels}
}

func (s *Server) CreateJob(ctx context.Context, r *imagev1.CreateJobRequest) (*imagev1.CreateJobResponse, error) {
	if int64(len(r.GetImage())) == 0 {
		return nil, status.Error(codes.InvalidArgument, "image is required")
	}
	if int64(len(r.GetImage())) > s.maxBytes {
		return nil, status.Errorf(codes.ResourceExhausted, "image exceeds %d bytes", s.maxBytes)
	}
	filename := filepath.Base(strings.ReplaceAll(r.GetFilename(), "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "image"
	}
	if len(filename) > 128 {
		return nil, status.Error(codes.InvalidArgument, "filename is too long")
	}
	format := r.GetOutputFormat()
	if format == imagev1.OutputFormat_OUTPUT_FORMAT_UNSPECIFIED {
		format = imagev1.OutputFormat_OUTPUT_FORMAT_JPEG
	}
	if format != imagev1.OutputFormat_OUTPUT_FORMAT_JPEG && format != imagev1.OutputFormat_OUTPUT_FORMAT_PNG {
		return nil, status.Error(codes.InvalidArgument, "output_format must be JPEG or PNG")
	}
	op := r.GetOperation()
	if op == imagev1.Operation_OPERATION_UNSPECIFIED {
		op = imagev1.Operation_OPERATION_THUMBNAIL
	}
	if op < imagev1.Operation_OPERATION_RESIZE || op > imagev1.Operation_OPERATION_THUMBNAIL {
		return nil, status.Error(codes.InvalidArgument, "unsupported operation")
	}
	if op == imagev1.Operation_OPERATION_RESIZE && (r.GetWidth() == 0 || r.GetHeight() == 0 || r.GetWidth() > 10000 || r.GetHeight() > 10000) {
		return nil, status.Error(codes.InvalidArgument, "resize width and height must be between 1 and 10000")
	}
	if op == imagev1.Operation_OPERATION_RESIZE && int64(r.GetWidth())*int64(r.GetHeight()) > s.maxPixels {
		return nil, status.Error(codes.InvalidArgument, "requested output dimensions exceed the pixel limit")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(r.GetImage()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid or unsupported image; use JPEG or PNG")
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > s.maxPixels {
		return nil, status.Error(codes.InvalidArgument, "image dimensions exceed the pixel limit")
	}
	var raw [16]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return nil, status.Error(codes.Internal, "could not create job id")
	}
	id := hex.EncodeToString(raw[:])
	path := filepath.Join(s.dataDir, "inputs", id+".img")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, status.Error(codes.Internal, "could not prepare image storage")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not save image")
	}
	_, copyErr := f.Write(r.GetImage())
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(path)
		return nil, status.Error(codes.Internal, "could not save image")
	}
	j := store.Job{ID: id, Status: "QUEUED", InputPath: path, InputFilename: filename, Operation: op.String(), OutputFormat: format.String(), TargetWidth: r.GetWidth(), TargetHeight: r.GetHeight()}
	if err = s.db.Create(ctx, j); err != nil {
		os.Remove(path)
		return nil, status.Error(codes.Internal, "could not create job")
	}
	return &imagev1.CreateJobResponse{JobId: id, Status: imagev1.JobStatus_JOB_STATUS_QUEUED}, nil
}
func (s *Server) GetJob(ctx context.Context, r *imagev1.GetJobRequest) (*imagev1.GetJobResponse, error) {
	j, err := s.db.Get(ctx, r.GetJobId())
	if err != nil {
		return nil, mapStoreError(err)
	}
	return toProto(j), nil
}
func (s *Server) DownloadResult(ctx context.Context, r *imagev1.DownloadResultRequest) (*imagev1.DownloadResultResponse, error) {
	j, err := s.db.Get(ctx, r.GetJobId())
	if err != nil {
		return nil, mapStoreError(err)
	}
	if j.Status != "SUCCEEDED" {
		return nil, status.Error(codes.FailedPrecondition, "job has no completed result")
	}
	f, err := os.Open(j.ResultPath)
	if err != nil {
		return nil, status.Error(codes.Internal, "result file is unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, s.maxBytes*4+1))
	if err != nil {
		return nil, status.Error(codes.Internal, "could not read result")
	}
	if int64(len(b)) > s.maxBytes*4 {
		return nil, status.Error(codes.ResourceExhausted, "result is too large")
	}
	ct := "image/jpeg"
	if strings.HasSuffix(j.ResultFilename, ".png") {
		ct = "image/png"
	}
	return &imagev1.DownloadResultResponse{Image: b, Filename: j.ResultFilename, ContentType: ct}, nil
}
func mapStoreError(err error) error {
	if err == store.ErrNotFound {
		return status.Error(codes.NotFound, "job not found")
	}
	return status.Error(codes.Internal, "could not read job")
}
func toProto(j store.Job) *imagev1.GetJobResponse {
	st := map[string]imagev1.JobStatus{"QUEUED": imagev1.JobStatus_JOB_STATUS_QUEUED, "PROCESSING": imagev1.JobStatus_JOB_STATUS_PROCESSING, "SUCCEEDED": imagev1.JobStatus_JOB_STATUS_SUCCEEDED, "FAILED": imagev1.JobStatus_JOB_STATUS_FAILED}[j.Status]
	return &imagev1.GetJobResponse{JobId: j.ID, Status: st, InputFilename: j.InputFilename, ResultFilename: j.ResultFilename, Error: j.Error, CreatedAtUnix: j.CreatedAt.Unix(), UpdatedAtUnix: j.UpdatedAt.Unix(), Width: j.Width, Height: j.Height, ResultSizeBytes: j.ResultSize}
}
