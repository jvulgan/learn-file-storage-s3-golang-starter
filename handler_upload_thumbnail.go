package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video from the database", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Authenticated user is not the video owner", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to parse media type from the headers", nil)
		return
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Thumbnal must be an image", nil)
		return
	}
	fileExt, _ := strings.CutPrefix(mediaType, "image/")

	b := make([]byte, 32)
	rand.Read(b)
	randStr := base64.RawURLEncoding.EncodeToString(b)
	thumbFilepath := fmt.Sprintf("%s.%s", randStr, fileExt)
	path := filepath.Join(cfg.assetsRoot, thumbFilepath)
	f, err := os.Create(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create thumbnail file", err)
		return
	}
	_, err = io.Copy(f, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy contents of thumbnail file", err)
		return
	}

	tnUrl := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, thumbFilepath)
	video.ThumbnailURL = &tnUrl
	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update database record", err)
		return
	}

	fmt.Println("done uploading thumbnail for video", videoID, "by user", userID)
	respondWithJSON(w, http.StatusOK, video)
}
