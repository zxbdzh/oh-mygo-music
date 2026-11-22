package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// 领取结果仍校验服务端有效性；只有这次明确点击允许后续剪贴板或文件操作。
func (a *app) deliverBackgroundShare(draft *shareDraft, output shareOutput) {
	task := draft.backgroundTask
	if task == nil || task.removed || task.state != backgroundTaskSucceeded || draft.exporting || a.quitting {
		return
	}
	draft.exporting = true
	task.status = T("Checking existing shares…")
	client, metadata, cover, generation := draft.shareClient, *draft.metadata, draft.shareCover, task.generation
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		result, err := client.reuse(ctx, metadata, cover)
		a.post(func() {
			draft.exporting = false
			if a.quitting || task.removed || task.generation != generation {
				return
			}
			if err == nil && (result == nil || result.URL != draft.onlineURL) {
				err = errors.New(T("Share is no longer available. Retry to prepare it again."))
			}
			if err != nil {
				draft.onlineError = err
				task.state, task.status = backgroundTaskFailed, T("Could not verify share. Retry or open to edit.")
				task.retry = func() { a.retryBackgroundShare(draft) }
				return
			}
			task.status = T("Online link ready")
			switch output {
			case shareOutputCopyLink:
				a.copyPublishedShareLink(draft)
			case shareOutputCopyImage:
				a.copyPreparedShareImage(draft)
			case shareOutputSaveImage:
				a.savePreparedShareImage(draft)
			}
		})
	}()
}

// 系统写入边界可替换，自动验收不接触用户的剪贴板。
var copyImage = func(data []byte) error {
	return mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, data))))
}

var saveCardAs = func(a *app, name, directory string) (string, error) {
	if directory == "" {
		directory, _ = mygo.App.Path(mygo.PathDownloads)
	}
	return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: T("Save PNG"), DefaultPath: filepath.Join(directory, name),
		Filters: []mygo.FileFilter{{Name: "PNG", Extensions: []string{"png"}}}, CreateDirectories: true})
}

type shareOutput uint8

const (
	shareOutputCopyImage shareOutput = iota
	shareOutputSaveImage
	shareOutputCopyLink
)

func (a *app) prepareOnlineCardOutput(draft *shareDraft, output shareOutput) bool {
	if !draft.cardOnline && output != shareOutputCopyLink {
		return false
	}
	if draft.onlinePublishing || draft.exporting {
		return true
	}
	// 每次导出重新确认服务端有效期与撤销状态，不能仅凭本地 URL 判断。
	a.publishOnlineShare(draft)
	return true
}

func (a *app) saveShareImage(draft *shareDraft) {
	if a.prepareOnlineCardOutput(draft, shareOutputSaveImage) {
		return
	}
	a.savePreparedShareImage(draft)
}

func (a *app) savePreparedShareImage(draft *shareDraft) {
	if len(draft.png) == 0 || draft.exporting {
		return
	}
	data, name, directory := slices.Clone(draft.png), songFileName(&draft.track, "png"), a.settings.CardDirectory
	pick := saveCardAs
	draft.exporting, draft.status = true, ""
	go func() {
		path, err := pick(a, name, directory)
		if err == nil && path != "" {
			err = writeCardPNG(path, data)
		}
		a.post(func() {
			if err == nil && path != "" {
				a.settings.CardDirectory = filepath.Dir(path)
				if saveErr := a.settings.save(); saveErr != nil {
					a.toast = T("Could not save display preferences")
				}
			}
			if a.sharing != draft && draft.backgroundTask == nil {
				return
			}
			draft.exporting = false
			switch {
			case err != nil:
				draft.status = T("Could not save image")
			case path == "":
				draft.status = T("Save canceled")
			default:
				draft.status = T("Image saved")
			}
			if draft.backgroundTask != nil {
				draft.backgroundTask.status = draft.status
			}
		})
	}()
}

func writeCardPNG(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".mygo-card-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	// 完整编码并落盘后才替换目标，失败不会截断用户已有图片。
	return os.Rename(file.Name(), path)
}

func (a *app) copyShareImage(draft *shareDraft) {
	if a.prepareOnlineCardOutput(draft, shareOutputCopyImage) {
		return
	}
	a.copyPreparedShareImage(draft)
}

func (a *app) copyPreparedShareImage(draft *shareDraft) {
	if len(draft.png) == 0 || draft.exporting {
		return
	}
	data := slices.Clone(draft.png)
	draft.exporting, draft.status = true, ""
	copy := copyImage
	go func() {
		err := copy(data)
		a.post(func() {
			if a.sharing != draft && draft.backgroundTask == nil {
				return
			}
			draft.exporting = false
			draft.status = T("Image copied")
			if err != nil {
				draft.status = T("Could not copy image")
			}
			if draft.backgroundTask != nil {
				draft.backgroundTask.status = draft.status
			}
		})
	}()
}
