# Build environment for the Jenkins pipeline: Go, golangci-lint, gotestsum, ffmpeg.
FROM mwader/static-ffmpeg:9.0.2 AS ffmpeg
FROM golangci/golangci-lint:v2.14.0 AS lint

FROM golang:1.27.1
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=lint /usr/bin/golangci-lint /usr/local/bin/
RUN GOBIN=/usr/local/bin go install gotest.tools/gotestsum@v1.13.0 && rm -rf /root/go /root/.cache
