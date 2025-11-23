package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	split := strings.Split(*video.VideoURL, ",")
	if len(split) < 2 {
		return video, nil
	}
	bucket := split[0]
	key := split[1]
	presignedUrl, err := generatePresignedURL(cfg.s3Client, bucket, key, 5*time.Minute)
	if err != nil {
		return database.Video{}, err
	}
	video.VideoURL = &presignedUrl
	return video, nil
}
