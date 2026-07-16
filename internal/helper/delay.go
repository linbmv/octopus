package helper

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

func GetUrlDelay(httpClient *http.Client, url string, ctx context.Context) (int, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create delay request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	if err := resp.Body.Close(); err != nil {
		return 0, fmt.Errorf("close delay response body: %w", err)
	}
	return int(time.Since(start).Milliseconds()), nil
}
