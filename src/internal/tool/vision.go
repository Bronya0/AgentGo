// Package tool — 图片理解工具：通过 Vision API 分析图片内容。
package tool

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ImageUnderstand 返回图片理解工具。
// workspaceDir 用于限制本地文件读取范围。
func ImageUnderstand(workspaceDir string) Tool {
	client := &http.Client{Timeout: 30 * time.Second}

	return Tool{
		Name:        "image_understand",
		Description: "Analyze an image and describe its contents. Accepts a local file path (relative to workspace) or a public URL. The image will be sent to the LLM for visual understanding.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type":        "string",
					"description": "Image source: relative file path (e.g. 'images/diagram.png') or public URL (https://...)",
				},
				"question": map[string]any{
					"type":        "string",
					"description": "What to analyze or ask about the image (default: describe the image)",
				},
			},
			"required": []string{"source"},
		},
		Execute: func(ctx context.Context, args Args) Result {
			source, err := MustGetString(args, "source")
			if err != nil {
				return Errf("%v", err)
			}
			question := "Describe this image in detail."
			if q, ok := args["question"]; ok {
				if s, ok := q.(string); ok && s != "" {
					question = s
				}
			}

			var dataURI string
			if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
				// URL 模式：下载并转为 base64
				uri, fetchErr := fetchImageAsDataURI(ctx, client, source)
				if fetchErr != nil {
					return Errf("image_understand: %v", fetchErr)
				}
				dataURI = uri
			} else {
				// 本地文件模式
				abs, pathErr := safeJoin(workspaceDir, source)
				if pathErr != nil {
					return Errf("path traversal not allowed: %v", pathErr)
				}
				uri, readErr := localImageAsDataURI(abs)
				if readErr != nil {
					return Errf("image_understand: %v", readErr)
				}
				dataURI = uri
			}

			// 返回特殊标记，让 runner 知道这是需要用 vision 处理的内容
			return Result{
				Content: fmt.Sprintf("[IMAGE_VISION]\nquestion=%s\ndata_uri=%s", question, dataURI),
			}
		},
	}
}

// fetchImageAsDataURI 下载远程图片并转为 data URI。
func fetchImageAsDataURI(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d fetching image", resp.StatusCode)
	}

	const maxImageSize = 20 * 1024 * 1024 // 20MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = detectImageMIME(data)
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

// localImageAsDataURI 读取本地图片并转为 data URI。
func localImageAsDataURI(path string) (string, error) {
	const maxImageSize = 20 * 1024 * 1024
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if info.Size() > maxImageSize {
		return "", fmt.Errorf("image too large (%d bytes, max %d)", info.Size(), maxImageSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", path, err)
	}
	mime := detectImageMIME(data)
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

// detectImageMIME 根据文件头检测图片 MIME 类型。
func detectImageMIME(data []byte) string {
	if len(data) < 4 {
		return "application/octet-stream"
	}
	// PNG
	if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	// JPEG
	if data[0] == 0xFF && data[1] == 0xD8 {
		return "image/jpeg"
	}
	// GIF
	if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		return "image/gif"
	}
	// WebP
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}
	return "application/octet-stream"
}
