package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video from the database", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Authenticated user is not the video owner", nil)
		return
	}

	fmt.Println("uploading video by user", userID)

	// limit max size of the video
	const maxSize = int64(10 << 30)
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	file, header, err := r.FormFile("video")
	if err != nil {
		if err.Error() == "http: request body too large" {
			respondWithError(w, http.StatusRequestEntityTooLarge, "Video is too large", err)
		} else {
			respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		}
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to parse media type from the headers", nil)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Unsupported media type", nil)
		return
	}

	tmpFile, err := os.CreateTemp("", "tubely-upload.mp4")
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()
	if _, err := io.Copy(tmpFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to copy video data to a file", err)
		return
	}
	// reset tmpFile pointer to the beginning
	tmpFile.Seek(0, io.SeekStart)

	b := make([]byte, 32)
	rand.Read(b)
	randStr := base64.RawURLEncoding.EncodeToString(b)
	videoKey := fmt.Sprintf("%s.mp4", randStr)
	if _, err := cfg.s3Client.PutObject(
		context.Background(),
		&s3.PutObjectInput{
			Bucket:      &cfg.s3Bucket,
			Key:         &videoKey,
			Body:        tmpFile,
			ContentType: &mediaType,
		},
	); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload the video data to s3 bucket", err)
		return
	}

	vidUrl := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, videoKey)
	video.VideoURL = &vidUrl
	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update database record", err)
		return
	}

	fmt.Println("done uploading video by user", userID)
	respondWithJSON(w, http.StatusOK, video)
}
