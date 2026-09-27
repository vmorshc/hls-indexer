FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hls-indexer ./cmd/hls-indexer

FROM mwader/static-ffmpeg:9.0.2 AS ffmpeg

FROM gcr.io/distroless/static-debian13
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=build /out/hls-indexer /usr/local/bin/hls-indexer
# The binary embeds these defaults. The file is a template for --config overrides.
COPY config/default.yaml /etc/hls-indexer/default.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hls-indexer"]
CMD ["api"]
