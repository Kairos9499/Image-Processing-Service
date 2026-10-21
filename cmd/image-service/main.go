package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	imagev1 "image-processing-service/gen/image/v1"
	"image-processing-service/internal/processor"
	"image-processing-service/internal/service"
	"image-processing-service/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: image-service api|worker|health")
	}
	if os.Args[1] == "health" {
		addr := env("GRPC_ADDR", ":50051")
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			log.Print("API is not accepting connections: ", err)
			os.Exit(1)
		}
		_ = conn.Close()
		return
	}
	dataDir := env("DATA_DIR", "./data")
	if err := os.MkdirAll(filepath.Join(dataDir, "inputs"), 0750); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "results"), 0750); err != nil {
		log.Fatal(err)
	}
	maxBytes := int64(envInt("MAX_IMAGE_BYTES", 10<<20))
	maxPixels := int64(envInt("MAX_PIXELS", 40_000_000))
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "api":
		addr := env("GRPC_ADDR", ":50051")
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatal(err)
		}
		server := grpc.NewServer(grpc.MaxRecvMsgSize(int(maxBytes+(1<<20))), grpc.MaxSendMsgSize(int(maxBytes+(1<<20))*4))
		imagev1.RegisterImageServiceServer(server, service.New(db, dataDir, maxBytes, maxPixels))
		healthServer := health.NewServer()
		healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		healthpb.RegisterHealthServer(server, healthServer)
		go func() {
			<-ctx.Done()
			healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
			server.GracefulStop()
		}()
		log.Printf("gRPC API listening on %s", addr)
		if err := server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Fatal(err)
		}
	case "worker":
		interval, err := time.ParseDuration(env("WORKER_POLL_INTERVAL", "1s"))
		if err != nil || interval <= 0 {
			log.Fatal("invalid WORKER_POLL_INTERVAL")
		}
		worker := processor.New(db, dataDir, maxPixels)
		log.Print("image worker started")
		if err := worker.Run(ctx, interval); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown mode %q", os.Args[1])
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Fatalf("%s must be a positive integer", key)
	}
	return n
}
