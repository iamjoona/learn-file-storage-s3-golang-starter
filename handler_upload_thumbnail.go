package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	var contentTypeToExt = map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
	}

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20 // 10MB using bit shifting
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", nil)
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't read file", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "No access right to this video", nil)
		return
	}

	ext, ok := contentTypeToExt[mediaType]
	if !ok {
		respondWithError(w, http.StatusBadRequest, "Unsupported image type", nil)
		return
	}

	key := make([]byte, 32)
	_, err = rand.Read(key)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate random name", err)
		return
	}
	base64Name := base64.RawURLEncoding.EncodeToString(key)
	filename := fmt.Sprintf("%s%s", base64Name, ext)
	filepath := filepath.Join(cfg.assetsRoot, filename)

	thumbnailFile, err := os.Create(filepath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create thumbnail file", err)
		return
	}
	defer thumbnailFile.Close()

	if _, err := io.Copy(thumbnailFile, bytes.NewReader(data)); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy thumbnail file", err)
		return
	}

	thumbnailUrl := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)
	video.ThumbnailURL = &thumbnailUrl

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
