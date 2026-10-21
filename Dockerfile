FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache build-base
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/image-service ./cmd/image-service \
    && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/image-client ./cmd/image-client

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/image-service /usr/local/bin/image-service
COPY --from=build /out/image-client /usr/local/bin/image-client
USER app
EXPOSE 50051
ENTRYPOINT ["image-service"]
