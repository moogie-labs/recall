package app

import (
	"context"
	"path/filepath"

	"recall/internal/attachment"
	"recall/internal/model"
)

const maxNameAttempts = 999

// Attachment is a stored image and the Markdown destination that links to it
// from its note's folder.
type Attachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// SaveAttachment stores an image in the note's attachment folder, creating
// the folder if needed. The format comes from the bytes, the name is
// sanitised, and an existing file is never overwritten: clashes get -2, -3, ...
func (a *App) SaveAttachment(ctx context.Context, ref model.Ref, name string, data []byte) (Attachment, error) {
	if len(data) > attachment.MaxBytes {
		return Attachment{}, appError("invalid_argument", "", "images must be 20 MB or smaller")
	}
	format, ok := attachment.Detect(data)
	if !ok {
		return Attachment{}, appError("invalid_argument", "", "only PNG, JPEG, GIF and WebP images are supported")
	}
	var saved Attachment
	_, err := a.mutateDocument(ctx, ref, "", func(note model.Document) (model.Document, error) {
		folder := attachment.Folder(note.Path)
		// Mkdir refuses a symlink (outside_root). An existing regular file
		// reports already_exists here and is refused by Write's no-follow
		// directory open below (invalid_argument).
		if err := a.store.Mkdir(ctx, folder); err != nil && model.ErrorCode(err) != "already_exists" {
			return model.Document{}, err
		}
		for n := 1; n <= maxNameAttempts; n++ {
			file := attachment.FileName(name, format, n)
			path := filepath.Join(folder, file)
			err := a.store.Write(ctx, path, "", data)
			if model.ErrorCode(err) == "already_exists" {
				continue
			}
			if err != nil {
				return model.Document{}, err
			}
			saved = Attachment{Path: path, Name: file, Link: attachment.Destination(filepath.Join(filepath.Base(folder), file))}
			return note, nil
		}
		return model.Document{}, appError("already_exists", folder, "too many images share this name")
	})
	return saved, err
}

// AttachmentCount counts the files directly inside a note's attachment folder.
func (a *App) AttachmentCount(ctx context.Context, ref model.Ref) (int, error) {
	note, err := a.Read(ctx, ref)
	if err != nil {
		return 0, err
	}
	return a.countAttachments(ctx, attachment.Folder(note.Path))
}

func (a *App) countAttachments(ctx context.Context, folder string) (int, error) {
	entries, err := a.store.List(ctx, folder)
	if model.ErrorCode(err) == "not_found" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir && !entry.IsSymlink {
			count++
		}
	}
	return count, nil
}

// ReadAttachment returns an image stored directly inside an attachment folder.
// It reads through the confined, no-follow store and requires the bytes to
// match an accepted format and the file's extension, so nothing but genuine
// images in .assets folders is ever returned.
func (a *App) ReadAttachment(ctx context.Context, path string) ([]byte, attachment.Format, error) {
	if err := validatePath(path); err != nil {
		return nil, attachment.Format{}, err
	}
	notAttachment := appError("not_found", path, "not a note image")
	if !attachment.IsFolderName(filepath.Base(filepath.Dir(path))) {
		return nil, attachment.Format{}, notAttachment
	}
	want, ok := attachment.ForExtension(filepath.Ext(path))
	if !ok {
		return nil, attachment.Format{}, notAttachment
	}
	data, err := a.store.Read(ctx, path)
	if err != nil {
		return nil, attachment.Format{}, err
	}
	if got, ok := attachment.Detect(data); !ok || got != want {
		return nil, attachment.Format{}, notAttachment
	}
	return data, want, nil
}
