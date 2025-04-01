package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
)

func getVideoAspectRatio(filepath string) (string, error) {
	// Use ffprobe to get video aspect ratio
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filepath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("ffprobe error: %v, stderr: %s", err, stderr.String())
	}

	// unmarshal stdout to json
	type ffprobeOutput struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	var output ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return "", fmt.Errorf("json unmarshal error: %v, output: %s", err, stdout.String())
	}

	if len(output.Streams) == 0 {
		return "", fmt.Errorf("no streams found")
	}

	// calculate aspect ratio
	// allowed 16:9, 9:16 and other
	var videoWidth, videoHeight int
	for _, stream := range output.Streams {
		if stream.Width != 0 && stream.Height != 0 {
			videoWidth = stream.Width
			videoHeight = stream.Height
			break
		}
	}

	if videoWidth == 0 || videoHeight == 0 {
		return "", fmt.Errorf("no valid video dimensions found")
	}

	aspectRatio := float64(videoWidth) / float64(videoHeight)
	const tolerance = 0.1

	if math.Abs(aspectRatio-16.0/9.0) < tolerance {
		return "16:9", nil
	} else if math.Abs(aspectRatio-9.0/16.0) < tolerance {
		return "9:16", nil
	}
	return "other", nil

}
