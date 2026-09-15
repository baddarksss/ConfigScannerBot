package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MudfishUpload posts one link per line to bin.mudfish.net (Text Bin) and
// returns the public RAW link — https://bin.mudfish.net/r/<tid>.
// Mirrors the app's Mudfish output (POST /api/text, ttl = Never).
// API reverse-engineered from the site's own JS.
func MudfishUpload(links []string) (string, error) {
	if len(links) == 0 {
		return "", errors.New("no links to upload")
	}
	payload, err := json.Marshal(map[string]string{
		"text": strings.Join(links, "\n") + "\n",
		"ttl":  "0", // Never
	})
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post("https://bin.mudfish.net/api/text",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Status int    `json:"status"`
		TID    string `json:"tid"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("bad reply: %v", err)
	}
	if out.Status != 200 || out.TID == "" {
		if out.Error != "" {
			return "", errors.New(out.Error)
		}
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return "https://bin.mudfish.net/r/" + out.TID, nil
}
