package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"bike/enforce/internal/digest"
)

const maxPhotoBytes = 10 << 20 // 契约：不超过 10 MB

var photoExt = map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}

// readUploadedFile 从 multipart 请求里读出 file 字段，超过上限返回 INVALID。
func readUploadedFile(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errInvalid("请用表单上传照片文件")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, errInvalid("没有收到照片文件")
		}
		if err != nil {
			return nil, errInvalid("照片不能超过 10 MB")
		}
		if part.FormName() != "file" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, maxPhotoBytes+1))
		if err != nil {
			return nil, errInvalid("照片不能超过 10 MB")
		}
		if len(b) > maxPhotoBytes {
			return nil, errInvalid("照片不能超过 10 MB")
		}
		return b, nil
	}
}

func (a *App) handleUploadPhoto(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.officer(r, true)
	if err != nil {
		return err
	}
	content, err := readUploadedFile(w, r)
	if err != nil {
		return err
	}
	ctype := http.DetectContentType(content)
	ext, ok := photoExt[ctype]
	if !ok {
		return errInvalid("照片格式不支持，只收 JPEG 或 PNG 格式")
	}
	return a.runIdem(w, r, "photo:"+adm.AccountID, func(tx pgx.Tx) (int, any, error) {
		now := a.now()
		id := "PH" + now.Format("20060102150405") + randHex(4)
		file := id + ext
		path := filepath.Join(a.PhotoDir, file)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return 0, nil, err
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return 0, nil, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return 0, nil, err
		}
		sum := digest.Bytes(content)
		if _, err := tx.Exec(r.Context(), `INSERT INTO photos (photo_id, officer_id, sha256, size, content_type, file_name, uploaded_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, adm.AccountID, sum, len(content), ctype, file, now); err != nil {
			_ = os.Remove(path)
			return 0, nil, err
		}
		if err := a.audit(r.Context(), tx, personActor(adm.AccountID, adm.Name), "上传取证照片", "evidence", id, "上传取证照片", nil); err != nil {
			_ = os.Remove(path)
			return 0, nil, err
		}
		return http.StatusCreated, map[string]any{"photoId": id, "sha256": sum, "size": len(content), "contentType": ctype, "uploadedAt": fmtTime(now)}, nil
	})
}

func (a *App) handleGetPhoto(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.admin(r)
	if err != nil {
		return err
	}
	isOfficer := adm.Role == roleOfficer
	if !isOfficer && !adm.HasAnyPerm("evidence.view") {
		return errForbidden(adm.Role)
	}
	ctx := r.Context()
	var owner, ctype, file string
	var evOperator *string
	err = a.Pool.QueryRow(ctx, `SELECT p.officer_id, p.content_type, p.file_name,
			(SELECT e.operator_id FROM evidence_photos ep JOIN evidence e USING (evidence_id) WHERE ep.photo_id = p.photo_id)
		FROM photos p WHERE p.photo_id = $1`, r.PathValue("photoId")).Scan(&owner, &ctype, &file, &evOperator)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound("照片")
	}
	if err != nil {
		return err
	}
	if isOfficer && owner != adm.AccountID {
		return &apiErr{http.StatusForbidden, "FORBIDDEN", "只能查看本人上传的照片"}
	}
	if !isOfficer && adm.Scope != "" && (evOperator == nil || *evOperator != adm.Scope) {
		return errScopeDenied()
	}
	b, err := os.ReadFile(filepath.Join(a.PhotoDir, file))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound("照片")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return nil
}

// CleanupOrphanPhotos 删除上传超过保留期、仍未被证据引用的照片（数据库行与文件），返回删除张数。
func (a *App) CleanupOrphanPhotos(ctx context.Context) (int, error) {
	cutoff := a.now().Add(-a.OrphanTTL)
	rows, err := a.Pool.Query(ctx, `DELETE FROM photos p WHERE p.uploaded_at < $1
		AND NOT EXISTS (SELECT 1 FROM evidence_photos ep WHERE ep.photo_id = p.photo_id) RETURNING file_name`, cutoff)
	if err != nil {
		return 0, err
	}
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return 0, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, f := range files {
		if err := os.Remove(filepath.Join(a.PhotoDir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			a.Log.Warn("删除照片文件失败", "file", f, "err", err)
		}
	}
	return len(files), nil
}
