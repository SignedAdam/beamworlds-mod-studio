package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// CollectionFolder is an app-owned generated view, not a user export. Its
// entries follow the deployment policy and are tracked individually so user
// files placed beside them are never swept away during refresh or rename.
type CollectionFolder struct {
	CollectionID string `json:"collectionId"`
	Name string `json:"name"`
	Path string `json:"path"`
	ModCount int `json:"modCount"`
	AddedCount int `json:"addedCount"`
	CopiedCount int `json:"copiedCount"`
	RemovedCount int `json:"removedCount"`
	SkippedMods []string `json:"skippedMods"`
}

const collectionFolderDirectory = "collections"

func (service *AppService) PlanCollectionFolder(ctx context.Context, collectionID string) (ArchiveDeploymentPlan,error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	_,plan,err:=service.collectionFolderPlan(ctx,collectionID)
	return plan,err
}

func (service *AppService) OpenCollectionFolder(ctx context.Context, collectionID, deploymentFingerprint string, allowCopy bool) (CollectionFolder,error) {
	folder,err:=service.syncCollectionFolder(ctx,collectionID,deploymentFingerprint,allowCopy)
	if err!=nil { return CollectionFolder{},err }
	if err:=openFolderInFileManager(folder.Path);err!=nil { return folder,fmt.Errorf("open %s in the file manager: %w",folder.Path,err) }
	_ = service.store.AppendEvent(context.Background(),"","collection_folder_opened",map[string]any{"collectionId":folder.CollectionID,"path":folder.Path,"modCount":folder.ModCount,"added":folder.AddedCount,"copied":folder.CopiedCount,"removed":folder.RemovedCount})
	return folder,nil
}

// Caller holds modImportMu. The collection ID, not a short timestamp prefix,
// owns the generated root. A copied mirror gets the same review as Play.
func (service *AppService) collectionFolderPlan(ctx context.Context,collectionID string) (CollectionDetail,ArchiveDeploymentPlan,error) {
	id:=strings.TrimSpace(collectionID)
	if id=="" { return CollectionDetail{},ArchiveDeploymentPlan{},errors.New("collection ID is required") }
	if strings.TrimSpace(service.config.ExportDir)=="" { return CollectionDetail{},ArchiveDeploymentPlan{},errors.New("export directory is not configured") }
	detail,err:=service.store.CollectionDetail(ctx,id)
	if err!=nil { return detail,ArchiveDeploymentPlan{},err }
	selection,err:=service.store.ResolvePlaySelection(ctx,[]string{id},nil)
	if err!=nil { return detail,ArchiveDeploymentPlan{},err }
	label:=sanitizeArchiveLabel(detail.Collection.Name)
	if label=="" { label="collection" }
	if runes:=[]rune(label);len(runes)>100 { label=string(runes[:100]) }
	base:=filepath.Join(service.config.ExportDir,collectionFolderDirectory)
	target:=filepath.Join(base,id,label)
	if !pathWithin(target,base) || pathWithin(target,service.config.ActiveModsDir) {
		return detail,ArchiveDeploymentPlan{},errors.New("collection mirrors must stay in the configured exports area, outside BeamNG's active mods folder")
	}
	plan,err:=service.buildArchiveDeploymentPlan(ctx,selection,target,archivePurposeCollection,id)
	return detail,plan,err
}

func (service *AppService) syncCollectionFolder(ctx context.Context,collectionID,reviewedFingerprint string,allowCopy bool) (CollectionFolder,error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if err:=service.requireGameStopped();err!=nil { return CollectionFolder{},err }
	detail,plan,err:=service.collectionFolderPlan(ctx,collectionID)
	if err!=nil { return CollectionFolder{},err }
	if reviewedFingerprint=="" || reviewedFingerprint!=plan.Fingerprint {
		return CollectionFolder{},errors.New("the collection folder plan changed; review it again before preparing files")
	}
	if len(plan.Blockers)>0 { return CollectionFolder{},fmt.Errorf("collection folder blocked: %s",strings.Join(plan.Blockers,"; ")) }
	if plan.RequiresCopyConfirmation && !allowCopy {
		return CollectionFolder{},fmt.Errorf("this collection folder requires %d bytes of copies; review and confirm the copy cost",plan.CopyBytes)
	}
	previous,err:=service.store.listOwnedArchiveEntries(ctx)
	if err!=nil { return CollectionFolder{},err }
	result,err:=service.applyArchiveDeployment(ctx,plan,service.requireGameStopped,nil)
	if err!=nil { return CollectionFolder{},err }
	// Only remove empty old label directories. Foreign files stay at their
	// original paths and their directories remain visible for the user.
	seen:=map[string]bool{}
	ownerRoot:=filepath.Join(service.config.ExportDir,collectionFolderDirectory,detail.Collection.ID)
	for _,entry:=range previous {
		if entry.Purpose!=archivePurposeCollection || entry.OwnerID!=detail.Collection.ID || samePath(entry.TargetRoot,plan.DestinationRoot) || seen[entry.TargetRoot] { continue }
		seen[entry.TargetRoot]=true
		if !pathWithin(entry.TargetRoot,ownerRoot) { continue }
		resolved,err:=filepath.EvalSymlinks(entry.TargetRoot)
		if err==nil && samePath(resolved,entry.TargetRoot) { _=os.Remove(entry.TargetRoot) }
	}
	return CollectionFolder{CollectionID:detail.Collection.ID,Name:detail.Collection.Name,Path:plan.DestinationRoot,ModCount:len(plan.Entries),AddedCount:result.Linked+result.Copied,CopiedCount:result.Copied,RemovedCount:result.Removed,SkippedMods:[]string{}},nil
}

func openFolderInFileManager(path string) error {
	if app:=application.Get();app!=nil && app.Env!=nil { return app.Env.OpenFileManager(path,false) }
	executable,arguments:=nativeFileManagerFolderCommand(runtime.GOOS,path)
	command:=exec.Command(executable,arguments...)
	if err:=command.Start();err!=nil { return fmt.Errorf("%s: %w",executable,err) }
	return command.Process.Release()
}
