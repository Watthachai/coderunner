package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/attach"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// The pictures an edit request carries (docs/edit-attachments.md, agreed with DV on 19 Nov
// 2026). They are kept in a folder of the job's own, outside every repository, from before
// the job is queued until the build ends; the agent is told where they are, and a build
// that holds a copy of one is not committed.

// SetAttachmentsDir is where CRN keeps the pictures edit requests carry.
func (m *manager) SetAttachmentsDir(dir string) { m.attach = attach.NewStore(dir) }

// keepPictures writes a job's pictures; a request without pictures is nothing to do.
func (m *manager) keepPictures(job uuid.UUID, pics []domain.Attachment) error {
	if len(pics) == 0 {
		return nil
	}
	if m.attach == nil {
		return errors.New("jobs: this CRN has nowhere to keep pictures (CRN_ATTACHMENTS_DIR)")
	}
	if err := m.attach.Save(job, pics); err != nil {
		return fmt.Errorf("jobs: keep the pictures: %w", err)
	}
	return nil
}

// checkPictures confirms a job's pictures are as they were received, before the agent runs.
func (m *manager) checkPictures(job uuid.UUID, pics []domain.Attachment) error {
	if m.attach == nil {
		return errors.New("this CRN keeps no pictures (CRN_ATTACHMENTS_DIR)")
	}
	return m.attach.Check(job, pics)
}

// dropPictures deletes a job's pictures. It is called whenever a job ends or never runs.
func (m *manager) dropPictures(job uuid.UUID, log *slog.Logger) {
	if m.attach == nil {
		return
	}
	n, err := m.attach.Drop(job)
	if err != nil {
		log.Error("delete the request's pictures failed", "job_id", job, "err", err)
		return
	}
	if n > 0 {
		log.Info("deleted the request's pictures", "job_id", job, "files", n)
	}
}

// sweepPictures runs at boot: a folder whose job is not waiting to run is deleted, and so
// is anything else in the store. A job CRN cannot look up keeps its pictures until the
// next boot.
func (m *manager) sweepPictures(ctx context.Context) {
	if m.attach == nil {
		return
	}
	gone, err := m.attach.Sweep(func(id uuid.UUID) bool {
		job, err := m.store.GetJob(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return false
		}
		return err != nil || job.Status == domain.JobQueued
	})
	if err != nil {
		m.logger.Error("sweep the request pictures failed", "err", err)
	}
	if len(gone) > 0 {
		m.logger.Info("deleted pictures no waiting job needs", "count", len(gone), "entries", gone)
	}
}

// pictureFor says what a picture belongs to.
func pictureFor(a domain.Attachment) string {
	if a.Item > 0 {
		return fmt.Sprintf("item %d", a.Item)
	}
	return "the whole request"
}

// picturesPrompt is what the edit prompt adds when the request carries pictures.
func picturesPrompt(store *attach.Store, job uuid.UUID, pics []domain.Attachment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nCRN received %d picture(s) with this request: screenshots from the people testing the app, "+
		"often with a pin or a box drawn on what they mean. \"item N\" is the request's item=N. "+
		"They are outside the project:\n", len(pics))
	for _, a := range pics {
		fmt.Fprintf(&b, "- %s: %s\n", pictureFor(a), store.Path(job, a))
	}
	b.WriteString("Open each one with your Read tool, which shows you the picture, before you change anything, " +
		"and use what it shows together with the text. Do not copy them into the project, commit them or put them in " +
		"the image: CRN fails a build that holds a copy of one, under any name. CRN deletes them when the build ends.\n")
	return b.String()
}

// picturesReceived is the trace's line on the pictures a build came with.
func picturesReceived(pics []domain.Attachment) string {
	parts := make([]string, len(pics))
	for i, a := range pics {
		belongs := "ทั้งคำขอ"
		if a.Item > 0 {
			belongs = fmt.Sprintf("item %d", a.Item)
		}
		parts[i] = fmt.Sprintf("%s (%s, %d bytes, sha256 %s…)", a.File, belongs, a.Size, a.SHA256[:12])
	}
	return fmt.Sprintf("CRN ได้รับรูป %d รูปกับคำขอนี้: %s", len(pics), strings.Join(parts, ", "))
}
