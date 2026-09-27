package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// The image and compose are checked statically. `docker compose up --build` is the live check.

func TestDockerfile(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"FROM golang:1.27.1 AS build",
		"CGO_ENABLED=0 go build",
		"FROM mwader/static-ffmpeg:9.0.2 AS ffmpeg",
		"COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/",
		"FROM gcr.io/distroless/static",
		"COPY config/default.yaml ",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}

func TestCompose(t *testing.T) {
	b, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Image    string   `yaml:"image"`
			Command  []string `yaml:"command"`
			Volumes  []string `yaml:"volumes"`
			Profiles []string `yaml:"profiles"`
		} `yaml:"services"`
	}
	if err := yaml.Load(b, &c); err != nil {
		t.Fatal(err)
	}
	s := c.Services
	if cmd := strings.Join(s["redis"].Command, " "); !strings.Contains(cmd, "--appendonly yes") {
		t.Errorf("redis command %q: want AOF on", cmd)
	}
	for _, role := range []string{"api", "worker"} {
		svc := s[role]
		if svc.Image != "hls-indexer" || !slices.Equal(svc.Command, []string{role}) || !slices.Contains(svc.Volumes, "./data:/data") {
			t.Errorf("%s: %+v", role, svc)
		}
	}
	if !slices.Contains(s["redis-test"].Profiles, "test") {
		t.Errorf("redis-test must be test-only: %+v", s["redis-test"])
	}
}
