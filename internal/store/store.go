package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("job not found")

type Job struct {
	ID, Status, InputPath, InputFilename, ResultPath, ResultFilename, Operation, OutputFormat, Error string
	Width, Height, TargetWidth, TargetHeight                                                         uint32
	CreatedAt, UpdatedAt                                                                             time.Time
	ResultSize                                                                                       int64
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY, status TEXT NOT NULL, input_path TEXT NOT NULL, input_filename TEXT NOT NULL,
		result_path TEXT NOT NULL DEFAULT '', result_filename TEXT NOT NULL DEFAULT '', operation TEXT NOT NULL,
		output_format TEXT NOT NULL, width INTEGER NOT NULL DEFAULT 0, height INTEGER NOT NULL DEFAULT 0,
		target_width INTEGER NOT NULL DEFAULT 0, target_height INTEGER NOT NULL DEFAULT 0,
		result_size INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
	); CREATE INDEX IF NOT EXISTS jobs_status_created ON jobs(status, created_at);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Create(ctx context.Context, j Job) error {
	now := time.Now().UTC()
	j.CreatedAt, j.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,status,input_path,input_filename,operation,output_format,target_width,target_height,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Status, j.InputPath, j.InputFilename, j.Operation, j.OutputFormat, j.TargetWidth, j.TargetHeight, now.Unix(), now.Unix())
	return err
}
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	var j Job
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id,status,input_path,input_filename,result_path,result_filename,operation,output_format,width,height,target_width,target_height,result_size,error,created_at,updated_at FROM jobs WHERE id=?`, id).Scan(&j.ID, &j.Status, &j.InputPath, &j.InputFilename, &j.ResultPath, &j.ResultFilename, &j.Operation, &j.OutputFormat, &j.Width, &j.Height, &j.TargetWidth, &j.TargetHeight, &j.ResultSize, &j.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return j, nil
}

// Claim atomically reserves the oldest queued job, so multiple workers cannot process it twice.
func (s *Store) Claim(ctx context.Context) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE status='QUEUED' ORDER BY created_at LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status='PROCESSING',updated_at=? WHERE id=? AND status='QUEUED'`, time.Now().UTC().Unix(), id)
	if err != nil {
		return Job{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, err
	}
	if n != 1 {
		return Job{}, ErrNotFound
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, id)
}
func (s *Store) Complete(ctx context.Context, id, path, name string, w, h uint32, size int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='SUCCEEDED',result_path=?,result_filename=?,width=?,height=?,result_size=?,error='',updated_at=? WHERE id=?`, path, name, w, h, size, time.Now().UTC().Unix(), id)
	return err
}
func (s *Store) SetTarget(ctx context.Context, id string, w, h uint32) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET target_width=?,target_height=? WHERE id=?`, w, h, id)
	return err
}
func (s *Store) Fail(ctx context.Context, id string, cause error) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='FAILED',error=?,updated_at=? WHERE id=?`, cause.Error(), time.Now().UTC().Unix(), id)
	return err
}
