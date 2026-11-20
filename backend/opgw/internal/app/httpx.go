package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"
)

// apiError 统一错误体（_common.yaml 的 Error）。
type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Errors  []rowError     `json:"errors,omitempty"`
	Extra   map[string]any `json:"-"`
}

type rowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Message: msg})
}

func serverBusy(w http.ResponseWriter) {
	writeErr(w, http.StatusInternalServerError, "SERVER_BUSY", "平台暂时不可用，请稍后重试")
}

// readBody 读请求体原文（上限 16MB，车辆清单最多 5000 行）。
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 16<<20))
}

// utf8Body 请求体必须是 UTF-8（Windows 命令行直传中文会被转成 GBK），否则 400 INVALID。
func utf8Body(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			const limit = 16 << 20
			b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "INVALID", "请求体读取失败")
				return
			}
			if len(b) <= limit && !utf8.Valid(b) {
				writeErr(w, http.StatusBadRequest, "INVALID", "请求体不是 UTF-8 编码（命令行直传中文会被转成本地代码页，请改用文件传：curl --data-binary @body.json）")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		h.ServeHTTP(w, r)
	})
}
