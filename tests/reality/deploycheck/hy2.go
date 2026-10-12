package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func fetchHY2(dir string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	for index, grant := range []string{"grant", "grant2"} {
		deadline := time.Now().Add(45 * time.Second)
		var payload []byte
		for time.Now().Before(deadline) {
			response, err := client.Get("http://controller:8088/subscription/docker-test-subscription?format=hysteria2&grant=" + grant)
			if err == nil {
				data, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr == nil && response.StatusCode == 200 {
					payload = data
					break
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		if len(payload) == 0 {
			return fmt.Errorf("HY2 Grant %s was not published", grant)
		}
		archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			return err
		}
		folder := filepath.Join(dir, fmt.Sprintf("hy2client%d", index+1))
		if err := os.MkdirAll(folder, 0700); err != nil {
			return err
		}
		for _, file := range archive.File {
			if file.Name != "client.json" && file.Name != "gateway.pem" && file.Name != "README.txt" {
				return fmt.Errorf("unexpected bundle file")
			}
			reader, err := file.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("PRIVATE KEY")) {
				return fmt.Errorf("bundle leaked private key")
			}
			if file.Name == "client.json" {
				if bytes.Contains(data, []byte("insecure")) || !bytes.Contains(data, []byte("pinSHA256")) {
					return fmt.Errorf("unsafe HY2 client export")
				}
				var config map[string]any
				if err := json.Unmarshal(data, &config); err != nil {
					return err
				}
				config["socks5"] = map[string]any{"listen": "0.0.0.0:1080"}
				if err := writeJSON(filepath.Join(folder, file.Name), config); err != nil {
					return err
				}
			} else if err := os.WriteFile(filepath.Join(folder, file.Name), data, 0600); err != nil {
				return err
			}
		}
	}
	response, err := client.Get("http://controller:8088/subscription/docker-test-subscription")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	plain, decodeErr := base64.StdEncoding.DecodeString(string(data))
	if response.StatusCode != 200 || decodeErr != nil || strings.Contains(string(plain), "insecure") || strings.Count(string(plain), "hysteria2://") != 2 || strings.Count(string(plain), "vmess://") != 2 {
		return fmt.Errorf("subscription unavailable")
	}
	return nil
}
