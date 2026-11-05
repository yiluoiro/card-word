package store

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type Announcement struct {
	Source                              string
	ID, Revision, CreatedAt, UpdatedAt  int64
	Title, Body, Status, Audience, Mode string
}

func (d *DB) SaveAnnouncement(ctx context.Context, a Announcement) (int64, error) {
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" || utf8.RuneCountInString(a.Title) > 120 || strings.TrimSpace(a.Body) == "" || len(a.Body) > 100*1024 {
		return 0, errors.New("标题最多 120 字，正文不能为空且不能超过 100 KB")
	}
	if a.Status != "draft" && a.Status != "published" && a.Status != "archived" {
		return 0, ErrInvalidStatus
	}
	if a.Audience != "all" && a.Audience != "guest" && a.Audience != "user" {
		return 0, errors.New("请选择可见人群")
	}
	if a.Mode != "silent" && a.Mode != "popup" {
		return 0, errors.New("请选择展示方式")
	}
	now := nowUnix()
	if a.ID == 0 {
		res, err := d.write.ExecContext(ctx, `INSERT INTO announcements(title,body,status,audience,mode,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, a.Title, a.Body, a.Status, a.Audience, a.Mode, now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := d.write.ExecContext(ctx, `UPDATE announcements SET title=?,body=?,status=?,audience=?,mode=?,revision=revision+1,updated_at=? WHERE id=?`, a.Title, a.Body, a.Status, a.Audience, a.Mode, now, a.ID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, ErrNotFound
	}
	return a.ID, nil
}
func (d *DB) Announcements(ctx context.Context, search, status, audience, mode string) ([]Announcement, error) {
	rows, _, err := d.AnnouncementPage(ctx, search, status, audience, mode, 0, 0)
	return rows, err
}
func (d *DB) AnnouncementByID(ctx context.Context, id int64) (Announcement, error) {
	var a Announcement
	err := d.read.QueryRowContext(ctx, `SELECT id,title,body,status,audience,mode,revision,created_at,updated_at FROM announcements WHERE id=?`, id).Scan(&a.ID, &a.Title, &a.Body, &a.Status, &a.Audience, &a.Mode, &a.Revision, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}
func (d *DB) AnnouncementPage(ctx context.Context, search, status, audience, mode string, limit, offset int) ([]Announcement, int, error) {
	q := ` FROM announcements WHERE 1=1`
	args := []any{}
	if search != "" {
		q += ` AND instr(lower(title),lower(?))>0`
		args = append(args, search)
	}
	for _, v := range []struct{ k, v string }{{"status", status}, {"audience", audience}, {"mode", mode}} {
		if v.v != "" {
			q += " AND " + v.k + "=?"
			args = append(args, v.v)
		}
	}
	var total int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*)`+q, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT id,title,body,status,audience,mode,revision,created_at,updated_at` + q + ` ORDER BY id DESC`
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, max(offset, 0))
	}
	rows, err := d.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Announcement{}
	for rows.Next() {
		var a Announcement
		if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Status, &a.Audience, &a.Mode, &a.Revision, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}
func (d *DB) DeleteAnnouncement(ctx context.Context, id int64) error {
	res, err := d.write.ExecContext(ctx, `DELETE FROM announcements WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
