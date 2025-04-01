package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	// maxFileSize := http.MaxBytesReader(w, r.Body, 1<<30)
	const maxFileSize = 1 << 30 // 1GB using bit shifting
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize)

	// extract video ID from url
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// authenticate user
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// get video metadata from db
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video metadata", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User not authorized to upload video", nil)
		return
	}

	// parse video file from form data
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// validate uploaded file
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", nil)
		return
	}

	// save file temporarily to disk
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not write file to disk", err)
		return
	}

	// reset tempFile's file pointer to the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not reset file pointer", err)
		return
	}

	// get video aspect ratio
	videoAspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video aspect ratio", err)
		return
	}

	directory := ""
	switch videoAspectRatio {
	case "16:9":
		directory = "landscape"
	case "9:16":
		directory = "portrait"
	default:
		directory = "other"
	}

	// process video for fast start
	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		log.Printf("Fast start processing error: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Couldn't process video for fast start", err)
		return
	}
	defer os.Remove(processedFilePath)

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't open processed file", err)
		return
	}
	defer processedFile.Close()

	// upload file to S3

	key := getAssetPath(mediaType)
	key = filepath.Join(directory, key)
	video_url := fmt.Sprintf("%s,%s", cfg.s3Bucket, key)

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key),
		Body:        processedFile,
		ContentType: aws.String("video/mp4"),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't upload file to S3", err)
		return
	}

	// update VideoURL in db with S3 bucket and key
	// url := cfg.getObjectURL(key)
	video.VideoURL = &video_url
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	// generate presigned URL for video
	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate presigned URL", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)

}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	request, err := presignClient.PresignGetObject(context.Background(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(expireTime),
	)
	if err != nil {
		return "", fmt.Errorf("failed to presign request: %v", err)
	}

	return request.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}

	var bucket, key string

	// Check if URL is in bucket,key format or full URL format
	if strings.HasPrefix(*video.VideoURL, "https://") {
		// Parse full URL to get bucket and key
		urlStr := *video.VideoURL
		// Remove https:// prefix
		urlStr = strings.TrimPrefix(urlStr, "https://")
		// Split on first slash to separate domain and path
		parts := strings.SplitN(urlStr, "/", 2)
		if len(parts) != 2 {
			return video, fmt.Errorf("invalid S3 URL format")
		}

		// Extract bucket name from domain (e.g., "tubely-private-663.s3.eu-north-1.amazonaws.com")
		domainParts := strings.Split(parts[0], ".")
		if len(domainParts) < 1 {
			return video, fmt.Errorf("invalid S3 domain format")
		}
		bucket = domainParts[0]
		key = parts[1]
	} else {
		// Handle bucket,key format
		parts := strings.Split(*video.VideoURL, ",")
		if len(parts) != 2 {
			return video, fmt.Errorf("invalid video URL format: %s", *video.VideoURL)
		}
		bucket = parts[0]
		key = parts[1]
	}

	// Generate presigned URL
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Hour)
	if err != nil {
		return video, fmt.Errorf("failed to generate presigned URL: %v", err)
	}

	// Update the video with presigned URL
	video.VideoURL = &presignedURL
	return video, nil
}
