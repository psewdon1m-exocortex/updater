package engine

import (
	"errors"
	"io"
	"os"
	"updater/internal/model"
)

// Same saved-copy proof as single-image heads, checked against a scoped sealed
// stream instead of allocating or base64-decoding the whole multi-GiB ZIP.
func (e *Engine) startGroupDownloaded(request model.UpdateRequest, token string) (model.Job, error) {
	if request.Backup.SpoolID == "" || request.Backup.DataBase64 != "" || request.Backup.RestoreURL != "" {
		return model.Job{}, errors.New("a saved group ZIP must be streamed to the scoped volatile spool")
	}
	checked := request
	checked.Backup.Filename = "mastermind-backup.zip"
	receipt, err := decodeBackupReceipt(checked, token)
	if err != nil {
		return model.Job{}, err
	}
	// A lost response can be replayed even after the transient file was removed.
	if previous, ok := e.store.ByRequestID(request.RequestID); ok {
		if previous.HeadID != request.HeadID || previous.Service != "mastermind" || previous.Version != request.Version ||
			previous.BackupSHA256 != receipt.SHA256 || previous.BackupSpoolID != request.Backup.SpoolID ||
			previous.PreparationID != request.PreparationID {
			return model.Job{}, errors.New("request id is already in use")
		}
		return previous, nil
	}
	item, path, err := e.store.ValidatedSpool(request.HeadID, request.RequestID, request.Backup.SpoolID)
	if err != nil {
		return model.Job{}, err
	}
	if receipt.SHA256 != item.SHA256 || int64(receipt.Size) != item.Size {
		return model.Job{}, errors.New("the supplied ZIP differs from the operator-saved group backup")
	}
	file, err := os.Open(path)
	if err != nil {
		return model.Job{}, err
	}
	var magic [4]byte
	_, err = io.ReadFull(file, magic[:])
	file.Close()
	if err != nil || string(magic[:2]) != "PK" {
		return model.Job{}, errors.New("a standard ZIP is required")
	}
	request.Backup.Filename, request.Backup.SHA256 = "", ""
	return e.Start(request)
}

// Failed group mutations recover only from the exact original operator copy.
// A successful update uses a fresh Core snapshot/barrier for version rollback.
func (e *Engine) RollbackGroupSaved(jobID, spoolID string, saved bool) (model.Job, error) {
	job, ok := e.store.Get(jobID)
	if !ok || !saved || job.Service != "mastermind" || !job.MutationStarted || !job.RollbackAvailable ||
		(job.State != "FAILED" && job.State != "ROLLBACK_FAILED") {
		return model.Job{}, errors.New("select an interrupted own-head update and its saved ZIP")
	}
	item, _, err := e.store.ValidatedSpool(job.HeadID, job.RequestID, spoolID)
	if err != nil {
		return model.Job{}, err
	}
	if item.SHA256 != job.BackupSHA256 || item.Filename != job.BackupFilename {
		return model.Job{}, errors.New("the saved ZIP does not match this recovery")
	}
	path, err := e.store.ClaimSpool(job.HeadID, job.RequestID, spoolID, job.ID)
	if err != nil {
		return model.Job{}, err
	}
	job.BackupSpoolID, job.BackupPath = spoolID, path
	if err = e.store.Save(job); err != nil {
		return model.Job{}, err
	}
	return e.Rollback(jobID)
}
