package main

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
)

type StorageCategorySummary struct {
 Category string `json:"category"`
 Root string `json:"root"`
 FileCount int `json:"fileCount"`
 ApparentBytes int64 `json:"apparentBytes"`
 AllocatedBytes int64 `json:"allocatedBytes"`
 UnknownFiles int `json:"unknownFiles"`
}

func(service *AppService)accountProtectedStorage(ctx context.Context,audit *StorageAudit,progress *StorageProgress)error{
 type scope struct{category,root string}
 scopes:=[]scope{{"thumbnails",service.config.ImageCacheDir},{"workspaces",service.config.WorkspaceDir},{"exports",service.config.ExportDir},{"backups",filepath.Join(service.config.BeamNGRoot,"Backups")},{"metadata",service.config.DataDir},{"game-data",service.config.BeamNGRoot}}
 archivePaths:=make(map[string]StorageAuditItem,len(audit.Items))
 seen:=map[physicalFileKey]bool{};shared:=map[physicalFileKey]bool{}
 for _,item:=range audit.Items{
  archivePaths[archivePathKey(item.Path)]=item
  if item.Identity.IdentityKnown{key:=physicalFileKey{item.Identity.VolumeID,item.Identity.FileID};if seen[key]{shared[key]=true};seen[key]=true}
 }
 var covered []string
 count:=0
 for _,scope:=range scopes{
  if scope.category=="backups" && service.config.BeamNGRoot==""{continue}
  if scope.root=="" || scope.root=="."{continue}
  root:=filepath.Clean(scope.root)
  info,err:=os.Lstat(root);if errors.Is(err,os.ErrNotExist){continue};if err!=nil{audit.Warnings=append(audit.Warnings,err.Error());continue}
  if !info.IsDir() || info.Mode()&os.ModeSymlink!=0{audit.Warnings=append(audit.Warnings,"Protected reparse location was not traversed: "+root);continue}
  summary:=StorageCategorySummary{Category:scope.category,Root:root}
  categorySeen:=map[physicalFileKey]bool{}
  err=filepath.WalkDir(root,func(path string,entry os.DirEntry,walkErr error)error{
   if err:=ctx.Err();err!=nil{return err}
   if walkErr!=nil{audit.Warnings=append(audit.Warnings,fmt.Sprintf("Protected storage %s: %v",path,walkErr));return nil}
   if entry.IsDir(){
    if scope.category=="exports" && pathWithin(path,filepath.Join(service.config.ExportDir,collectionFolderDirectory)){return filepath.SkipDir}
    for _,prior:=range covered{if pathWithin(path,prior){return filepath.SkipDir}}
    if !samePath(path,root) && (strings.HasPrefix(strings.ToLower(entry.Name()),".beamworlds-") || samePath(path,service.config.LibraryDir)){return filepath.SkipDir}
    if scope.category=="game-data" && service.config.ProjectRoot!="" && !samePath(service.config.ProjectRoot,root) && pathWithin(path,service.config.ProjectRoot) && !pathWithin(path,service.config.DataDir){return filepath.SkipDir}
    return nil
   }
   if entry.Type()&os.ModeSymlink!=0{summary.UnknownFiles++;audit.AllocationEstimated=true;return nil}
   item,alreadyAudited:=archivePaths[archivePathKey(path)]
   if alreadyAudited && scope.category!="exports" && scope.category!="workspaces" && scope.category!="backups"{return nil}
   identity:=item.Identity
   if !alreadyAudited{
    var err error;identity,err=inspectArchiveFile(path)
    if err!=nil{summary.UnknownFiles++;audit.AllocationEstimated=true;return nil}
    if !identity.Regular{return nil}
   }
   allocated:=identity.AllocatedBytes
   if !identity.AllocationKnown{allocated=identity.SizeBytes}
   if !identity.IdentityKnown || !identity.AllocationKnown{summary.UnknownFiles++;audit.AllocationEstimated=true}
   summary.FileCount++;summary.ApparentBytes+=identity.SizeBytes
   key:=physicalFileKey{identity.VolumeID,identity.FileID}
   if !identity.IdentityKnown || !categorySeen[key]{summary.AllocatedBytes+=allocated;categorySeen[key]=true}
   if !alreadyAudited{
    audit.ApparentBytes+=identity.SizeBytes
    if !identity.IdentityKnown || !seen[key]{audit.UniqueAllocatedBytes+=allocated}else if !shared[key]{audit.SharedBytes+=allocated;shared[key]=true}
    if identity.IdentityKnown{seen[key]=true}
   }
   count++
   if count%256==0{progress.Phase="accounting";progress.Current=path;service.emitStorageProgress(*progress)}
   return nil
  })
  if err!=nil{return err}
  covered=append(covered,root)
  audit.ProtectedCategories=append(audit.ProtectedCategories,summary)
 }
 // Bind reviewed recovery intent to its configured destination and volume,
 // without including changing database/log sizes in the cleanup fingerprint.
 type location struct{Path,VolumeID,FileID string}
 locations:=[]location{}
 for _,root:=range []string{service.config.LibraryDir,service.config.ActiveModsDir,service.config.ExportDir}{
  identity,_:=inspectArchiveFile(nearestExistingDir(root));locations=append(locations,location{root,identity.VolumeID,identity.FileID})
 }
 payload,_:=json.Marshal(struct{Fingerprint string;Locations []location}{audit.Fingerprint,locations})
 hash:=sha256.Sum256(payload);audit.Fingerprint=hex.EncodeToString(hash[:])
 return nil
}

func(service *AppService)auditExplicitExports(ctx context.Context,scanned map[string]bool)([]StorageAuditItem,[]string,error){
 root:=service.config.ExportDir
 if root==""{return nil,nil,nil}
 var items []StorageAuditItem;var warnings []string
 err:=filepath.WalkDir(root,func(path string,entry os.DirEntry,walkErr error)error{
  if err:=ctx.Err();err!=nil{return err}
  if errors.Is(walkErr,os.ErrNotExist){return nil}
  if walkErr!=nil{warnings=append(warnings,walkErr.Error());return nil}
  if entry.IsDir(){if !samePath(path,root) && (pathWithin(path,filepath.Join(root,collectionFolderDirectory)) || strings.HasPrefix(entry.Name(),".beamworlds-")){return filepath.SkipDir};return nil}
  if !isModArchive(entry.Name()) || scanned[archivePathKey(path)]{return nil}
  identity,exists,err:=archiveIdentityIfPresent(path);if err!=nil{warnings=append(warnings,err.Error());return nil};if !exists{return nil}
  scanned[archivePathKey(path)]=true
  items=append(items,StorageAuditItem{ID:stableAuditItemID("export",path),Path:path,Purpose:"user-export",Classification:classUserExport,Reason:"Explicit exports and user files are never automatically cleaned",LogicalBytes:identity.SizeBytes,AllocatedBytes:identity.AllocatedBytes,LinkCount:identity.Links,Identity:identity})
  return nil
 })
 return items,warnings,err
}

func(service *AppService)auditRecoveryPreparations(ctx context.Context,journals []deploymentJournalEntry,scanned map[string]bool)([]StorageAuditItem,error){
 var items []StorageAuditItem
 for _,j:=range journals{
  if j.Purpose!=storageRecoveryPurpose || len(j.Plan.Entries)!=1{continue}
  if !samePath(j.StagingDir,filepath.Join(j.DestinationRoot,".beamworlds-recovery-"+j.OperationID)){return nil,errors.New("invalid recovery staging journal")}
  for _,prepared:=range j.PreparedEntries{
   if err:=ctx.Err();err!=nil{return nil,err}
   path:=filepath.Join(prepared.TargetRoot,prepared.RelativePath)
   if scanned[archivePathKey(path)]{continue}
   if err:=validateArchiveChild(path,j.StagingDir);err!=nil{return nil,err}
   identity,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return nil,err};if !exists{continue}
   if !sameArchiveFileID(identity,prepared.TargetIdentity){continue}
   complete:=sameArchiveObject(identity,prepared.TargetIdentity) && identity.SizeBytes==j.Plan.Entries[0].SizeBytes
   reason:="Interrupted recovery preparation; original data is protected"
   if complete{reason="Verified recovery preparation retained; may be promoted to canonical storage"}
   scanned[archivePathKey(path)]=true
   items=append(items,StorageAuditItem{ID:stableAuditItemID("recovery-staging",path),Path:path,SourcePath:prepared.SourcePath,Purpose:"recovery-staging",Classification:classLegacyCacheOnly,Reason:reason,SHA256:prepared.SHA256,LogicalBytes:identity.SizeBytes,AllocatedBytes:identity.AllocatedBytes,LinkCount:identity.Links,Identity:identity,RecoveryAllowed:complete})
  }
 }
 return items,nil
}
