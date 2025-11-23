package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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

	dir := ""
	aspectRatio, err := getVideoAspectRatio(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error determining video aspect ratio", err)
		return
	}
	switch aspectRatio {
	case "16:9":
		dir = "landscape"
	case "9:16":
		dir = "portrait"
	default:
		dir = "other"
	}

	b := make([]byte, 32)
	rand.Read(b)
	randStr := base64.RawURLEncoding.EncodeToString(b)
	videoKey := fmt.Sprintf(
		"%s,%s",
		cfg.s3Bucket,
		path.Join(dir, fmt.Sprintf("%s.mp4", randStr)),
	)

	processedPath, err := processVideoForFastStart(tmpFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to preprocess video", err)
		return
	}
	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open preprocessed video", err)
		return
	}
	defer processedFile.Close()
	defer os.Remove(processedPath)

	if _, err := cfg.s3Client.PutObject(
		context.Background(),
		&s3.PutObjectInput{
			Bucket:      &cfg.s3Bucket,
			Key:         &videoKey,
			Body:        processedFile,
			ContentType: &mediaType,
		},
	); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload the video data to s3 bucket", err)
		return
	}

	video.VideoURL = &videoKey
	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update database record", err)
		return
	}

	fmt.Println("done uploading video by user", userID)

	video, err = cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate signed URL for video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
		"ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	buf := bytes.Buffer{}
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}

	type ffProbeOut struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	var out ffProbeOut
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		return "", fmt.Errorf("could not parse ffprobe output: %v", err)
	}
	if len(out.Streams) == 0 {
		return "", fmt.Errorf("no video streams found")
	}
	w := out.Streams[0].Width
	h := out.Streams[0].Height
	if w == 16*h/9 {
		return "16:9", nil
	} else if h == 16*w/9 {
		return "9:16", nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outFilePath := filePath + ".processing"
	cmd := exec.Command(
		"ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outFilePath,
	)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %v", err)
	}
	return outFilePath, nil
}

func generatePresignedURL(
	s3Client *s3.Client,
	bucket, key string,
	expireTime time.Duration) (string, error) {
	c := s3.NewPresignClient(s3Client)
	presignedReq, err := c.PresignGetObject(
		context.Background(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(expireTime),
	)
	if err != nil {
		return "", fmt.Errorf("error getting presign obj: %v", err)
	}
	return presignedReq.URL, nil

}
