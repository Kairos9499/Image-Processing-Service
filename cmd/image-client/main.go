package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	imagev1 "image-processing-service/gen/image/v1"
)

const maxImageBytes = 10 << 20

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	in := bufio.NewReader(os.Stdin)
	fmt.Println("Обработка изображения")
	path, err := prompt(in, "Путь к изображению", "")
	if err != nil {
		return err
	}
	path = strings.Trim(path, "\"'")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл: %w", err)
	}
	if len(data) > maxImageBytes {
		return fmt.Errorf("файл больше %d MiB", maxImageBytes>>20)
	}

	fmt.Println("\nВыберите операцию:")
	fmt.Println("  1) Миниатюра (уместить в 512×512)")
	fmt.Println("  2) Изменить размер")
	fmt.Println("  3) Чёрно-белое изображение")
	choice, err := prompt(in, "Номер операции", "1")
	if err != nil {
		return err
	}
	operations := map[string]imagev1.Operation{"1": imagev1.Operation_OPERATION_THUMBNAIL, "2": imagev1.Operation_OPERATION_RESIZE, "3": imagev1.Operation_OPERATION_GRAYSCALE}
	op, ok := operations[choice]
	if !ok {
		return errors.New("выберите 1, 2 или 3")
	}
	var width, height uint32
	if op == imagev1.Operation_OPERATION_RESIZE {
		width, err = promptDimension(in, "Ширина")
		if err != nil {
			return err
		}
		height, err = promptDimension(in, "Высота")
		if err != nil {
			return err
		}
	}
	fmt.Println("\nФормат результата:")
	fmt.Println("  1) PNG")
	fmt.Println("  2) JPEG")
	formatChoice, err := prompt(in, "Номер формата", "1")
	if err != nil {
		return err
	}
	formats := map[string]imagev1.OutputFormat{"1": imagev1.OutputFormat_OUTPUT_FORMAT_PNG, "2": imagev1.OutputFormat_OUTPUT_FORMAT_JPEG}
	format, ok := formats[formatChoice]
	if !ok {
		return errors.New("выберите 1 или 2")
	}

	addr := os.Getenv("IMAGE_SERVICE_ADDR")
	if addr == "" {
		addr = "localhost:50051"
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("не удалось подключиться к %s: %w", addr, err)
	}
	defer conn.Close()
	client := imagev1.NewImageServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fmt.Printf("\nОтправляю изображение в %s...\n", addr)
	created, err := client.CreateJob(ctx, &imagev1.CreateJobRequest{Image: data, Filename: filepath.Base(path), Operation: op, Width: width, Height: height, OutputFormat: format})
	if err != nil {
		return fmt.Errorf("не удалось создать задание: %w", err)
	}
	fmt.Printf("Задание %s создано. Ожидаю обработку", created.GetJobId())
	var job *imagev1.GetJobResponse
	for {
		job, err = client.GetJob(ctx, &imagev1.GetJobRequest{JobId: created.GetJobId()})
		if err != nil {
			return fmt.Errorf("не удалось проверить статус: %w", err)
		}
		if job.GetStatus() == imagev1.JobStatus_JOB_STATUS_SUCCEEDED {
			break
		}
		if job.GetStatus() == imagev1.JobStatus_JOB_STATUS_FAILED {
			return fmt.Errorf("обработка не удалась: %s", job.GetError())
		}
		if job.GetStatus() != imagev1.JobStatus_JOB_STATUS_QUEUED && job.GetStatus() != imagev1.JobStatus_JOB_STATUS_PROCESSING {
			return fmt.Errorf("неожиданный статус задания: %s", job.GetStatus())
		}
		fmt.Print(".")
		select {
		case <-ctx.Done():
			return errors.New("истекло время ожидания обработки")
		case <-time.After(time.Second):
		}
	}
	fmt.Printf(" готово (%d×%d, %s)\n", job.GetWidth(), job.GetHeight(), humanSize(job.GetResultSizeBytes()))
	result, err := client.DownloadResult(ctx, &imagev1.DownloadResultRequest{JobId: created.GetJobId()})
	if err != nil {
		return fmt.Errorf("не удалось скачать результат: %w", err)
	}
	ext := filepath.Ext(result.GetFilename())
	if ext == "" {
		ext = ".png"
	}
	defaultPath := strings.TrimSuffix(path, filepath.Ext(path)) + "_processed" + ext
	outPath, err := prompt(in, "Куда сохранить результат", defaultPath)
	if err != nil {
		return err
	}
	outPath = strings.Trim(strings.TrimSpace(outPath), "\"'")
	if _, err := os.Stat(outPath); err == nil {
		overwrite, promptErr := prompt(in, "Файл уже существует. Перезаписать? (д/н)", "н")
		if promptErr != nil {
			return promptErr
		}
		if overwrite != "д" && !strings.EqualFold(overwrite, "да") {
			return errors.New("сохранение отменено")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("не удалось проверить путь результата: %w", err)
	}
	if err = os.WriteFile(outPath, result.GetImage(), 0644); err != nil {
		return fmt.Errorf("не удалось сохранить файл: %w", err)
	}
	fmt.Printf("Результат сохранён: %s\n", outPath)
	return nil
}

func prompt(in *bufio.Reader, label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Printf("%s [%s]: ", label, fallback)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", errors.New("значение не может быть пустым")
	}
	return value, nil
}

func promptDimension(in *bufio.Reader, label string) (uint32, error) {
	value, err := prompt(in, label+" (1–10000)", "")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n == 0 || n > 10000 {
		return 0, fmt.Errorf("%s должна быть числом от 1 до 10000", label)
	}
	return uint32(n), nil
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d Б", n)
	}
	return fmt.Sprintf("%.1f КиБ", float64(n)/1024)
}
