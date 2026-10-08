package main

import (
 "context"
 "crypto/sha256"
 "encoding/json"
 "encoding/hex"
 "errors"
 "fmt"
 "io"
 "os"
 "path/filepath"
 "strings"
 "time"

 modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Activation changes only individually owned files. Every no-clobber move is
// journaled before execution; unchanged copies stay put, including on filesystems
// without hardlinks. SQLite's applied state is the commit point, not a rename.
func (service *AppService) runArchiveDeployment(ctx context.Context, plan ArchiveDeploymentPlan, beforeActivate, commit func() error) (result ArchiveDeploymentResult, resultErr error) {
 if len(plan.Blockers)>0 { return result,fmt.Errorf("deployment blocked: %s",strings.Join(plan.Blockers,"; ")) }
 if err:=service.requireGameStopped();err!=nil{return result,err}
 if err:=service.recoverArchiveDeployment(ctx);err!=nil{return result,err}
 operationID:=plan.operationID
 if operationID==""{id,err:=modkit.NewID();if err!=nil{return result,err};operationID=id}
 result.OperationID=operationID
 j:=deploymentJournalEntry{ID:operationID,OperationID:operationID,State:journalStatePlanned,Purpose:plan.Purpose,OwnerID:plan.OwnerID,DestinationRoot:plan.DestinationRoot,Plan:plan,StartedAt:nowUTC()}
 workRoot:=service.archiveTransactionWorkRoot(plan.Purpose)
 j.StagingDir=filepath.Join(workRoot,"staging-"+operationID)
 j.PreviousDir=filepath.Join(workRoot,"previous-"+operationID)
 if err:=service.validateArchiveJournalPaths(j);err!=nil{return result,err}
 allOwned,err:=service.store.listOwnedArchiveEntries(ctx);if err!=nil{return result,err}
 priorByPath:=map[string]OwnedArchiveEntry{}
 for _,entry:=range allOwned {
  if entry.Purpose==plan.Purpose && entry.OwnerID==plan.OwnerID && entry.State==archiveStateActive {
   if err:=service.validateOwnedArchivePath(entry);err!=nil{return result,err}
   j.PriorOwned=append(j.PriorOwned,entry)
   priorByPath[archivePathKey(filepath.Join(entry.TargetRoot,entry.RelativePath))]=entry
  }
 }
 if plan.Purpose==archivePurposePlay {
  if blockers:=checkManagedRootOwnership(ctx,service.store,plan.DestinationRoot);len(blockers)>0{return result,errors.New(strings.Join(blockers,"; "))}
 }
 for _,entry:=range plan.Entries {
  if entry.Method==deployMethodJunction {
   // Junction destinations are validated differently: the parent must be a
   // real directory; an existing junction at the leaf is fine if it is owned.
   if err:=validateArchiveChild(entry.DestinationPath,plan.DestinationRoot);err!=nil{return result,err}
   if isDirectoryJunction(entry.DestinationPath) {
    prior,owned:=priorByPath[archivePathKey(entry.DestinationPath)]
    if !owned || prior.Method!=deployMethodJunction {return result,fmt.Errorf("preserved unowned junction: %s",entry.DestinationPath)}
   }
   continue
  }
  if err:=validateArchiveChild(entry.DestinationPath,plan.DestinationRoot);err!=nil{return result,err}
  current,exists,err:=archiveIdentityIfPresent(entry.DestinationPath);if err!=nil{return result,err}
  if exists {
   prior,owned:=priorByPath[archivePathKey(entry.DestinationPath)]
   if !owned || !sameArchiveObject(current,prior.TargetIdentity){return result,fmt.Errorf("preserved unowned or changed destination: %s",entry.DestinationPath)}
  }
 }
 if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err}
 applied:=false
 defer func(){
  if resultErr==nil || applied{return}
  recoveryCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),30*time.Second);defer cancel()
  if err:=service.rollbackArchiveJournal(recoveryCtx,j);err!=nil{resultErr=errors.Join(resultErr,fmt.Errorf("deployment recovery remains pending: %w",err))}
 }()
 for _,dir:=range []string{workRoot,plan.DestinationRoot}{if err:=os.MkdirAll(dir,0755);err!=nil{return result,err}}
 for _,dir:=range []string{j.StagingDir,j.PreviousDir}{if err:=os.Mkdir(dir,0755);err!=nil{return result,err}}
 stageVolume,err:=inspectArchiveFile(j.StagingDir);if err!=nil{return result,err}
 targetVolume,err:=inspectArchiveFile(plan.DestinationRoot);if err!=nil{return result,err}
 if !stageVolume.IdentityKnown || !targetVolume.IdentityKnown || stageVolume.VolumeID!=targetVolume.VolumeID{return result,errors.New("deployment staging and destination must be verified on the same volume")}

 stagePaths:=map[string]string{}
 unchanged:=map[string]bool{}
 newOwned:=make([]OwnedArchiveEntry,0,len(plan.Entries))
 for _,original:=range plan.Entries {
  if err:=ctx.Err();err!=nil{return result,err}
  entry:=original

  // Junction entries bypass the staging/hashing pipeline entirely.
  if entry.Method==deployMethodJunction {
   srcInfo,statErr:=os.Lstat(entry.SourcePath)
   if statErr!=nil{return result,fmt.Errorf("junction source folder missing: %w",statErr)}
   if !srcInfo.IsDir(){return result,fmt.Errorf("junction source is not a folder: %s",entry.SourcePath)}
   id,idErr:=modkit.NewID();if idErr!=nil{return result,idErr}
   if entry.Reuse {
    if !isDirectoryJunction(entry.DestinationPath) || !samePath(junctionTarget(entry.DestinationPath),filepath.Clean(entry.SourcePath)){
     return result,fmt.Errorf("reused junction changed: %s",entry.DestinationPath)
    }
    unchanged[archivePathKey(entry.DestinationPath)]=true
    result.Reused++
   } else {
    result.Linked++
   }
   newOwned=append(newOwned,makeOwnedEntry(id,plan,entry,plan.DestinationRoot,ArchiveFileIdentity{}))
   result.Entries=append(result.Entries,entry)
   continue
  }

  source,err:=inspectArchiveFile(entry.SourcePath);if err!=nil{return result,err}
  if !sameArchiveObject(source,entry.SourceIdentity){return result,fmt.Errorf("source changed after review: %s",entry.SourcePath)}
  if plan.Purpose==archivePurposePlay { service.emitPlayProgress(PlayProgress{OperationID:operationID,Phase:"materializing",Current:filepath.Base(entry.SourcePath),Completed:len(result.Entries),Total:len(plan.Entries),BytesCopied:result.CopiedBytes,BytesHashed:result.HashedBytes,TotalBytes:plan.CopyBytes}) }
  if entry.VerifySource && entry.Method!=deployMethodCopy {
   checksum,err:=modkit.FullSHA256(ctx,entry.SourcePath);if err!=nil{return result,err}
   result.HashedBytes+=entry.SizeBytes
   if entry.SHA256!="" && !strings.EqualFold(checksum,entry.SHA256){return result,fmt.Errorf("archive changed since indexing: %s",entry.SourcePath)}
   entry.SHA256=checksum
  }
  id,err:=modkit.NewID();if err!=nil{return result,err}
  if entry.Reuse {
   reusePath:=entry.ReusePath;if reusePath==""{reusePath=entry.DestinationPath}
   prior,ok:=priorByPath[archivePathKey(reusePath)]
   identity,exists,err:=archiveIdentityIfPresent(reusePath)
   if err!=nil{return result,err}
   if !ok || !exists || !sameArchiveObject(identity,prior.TargetIdentity) || !sameArchiveObject(identity,entry.TargetIdentity){return result,fmt.Errorf("reused archive changed: %s",reusePath)}
   entry.TargetIdentity=identity
   if samePath(reusePath,entry.DestinationPath){unchanged[archivePathKey(reusePath)]=true}
   result.Reused++
  } else {
   stage:=filepath.Join(j.StagingDir,id+stagingExtension)
   owned:=makeOwnedEntry(id,plan,entry,plan.DestinationRoot,ArchiveFileIdentity{})
   if entry.Method==deployMethodHardlink {
    owned.TargetIdentity=source
    j.PreparedEntries=append(j.PreparedEntries,owned)
    if err:=service.store.savePreparedArchiveEntry(ctx,j.ID,owned);err!=nil{return result,err}
    if err:=os.Link(entry.SourcePath,stage);err!=nil{return result,fmt.Errorf("hardlink %s: %w",entry.SourcePath,err)}
    identity,err:=inspectArchiveFile(stage);if err!=nil{return result,err}
    if !sameArchiveObject(identity,source){return result,errors.New("hardlink did not preserve source file identity")}
    result.Linked++
   } else if entry.Method==deployMethodCopy {
    file,err:=os.OpenFile(stage,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600);if err!=nil{return result,err}
    identity,inspectErr:=inspectArchiveFile(stage)
    if inspectErr!=nil{file.Close();return result,inspectErr}
    owned.TargetIdentity=identity
    j.PreparedEntries=append(j.PreparedEntries,owned)
    if err:=service.store.savePreparedArchiveEntry(ctx,j.ID,owned);err!=nil{file.Close();return result,err}
    copied,checksum,copyErr:=copyArchivePayload(ctx,entry.SourcePath,file,entry.SHA256,func(bytes int64){
     if plan.Purpose==archivePurposePlay{service.emitPlayProgress(PlayProgress{OperationID:operationID,Phase:"materializing",Current:filepath.Base(entry.SourcePath),Completed:len(result.Entries),Total:len(plan.Entries),BytesCopied:result.CopiedBytes+bytes,BytesHashed:result.HashedBytes+bytes,TotalBytes:plan.CopyBytes})}
    })
    closeErr:=file.Close()
    result.CopiedBytes+=copied
    result.HashedBytes+=copied
    if err:=errors.Join(copyErr,closeErr);err!=nil{return result,err}
    entry.SHA256=checksum
    result.Copied++
   } else {return result,fmt.Errorf("unsupported deployment method %q",entry.Method)}
   identity,err:=inspectArchiveFile(stage);if err!=nil{return result,err}
   entry.TargetIdentity=identity
   owned.TargetIdentity=identity;owned.SHA256=entry.SHA256
   j.PreparedEntries[len(j.PreparedEntries)-1]=owned
   if entry.Method==deployMethodCopy { if err:=service.store.savePreparedArchiveEntry(ctx,j.ID,owned);err!=nil{return result,err} }
   stagePaths[archivePathKey(entry.DestinationPath)]=stage
  }
  newOwned=append(newOwned,makeOwnedEntry(id,plan,entry,plan.DestinationRoot,entry.TargetIdentity))
  result.Entries=append(result.Entries,entry)
 }
 // Park every changed/removed owned entry before installing any replacement.
 // Junction entries are removed directly—they are lightweight reparse points.
 parked:=map[string]string{}
 for _,entry:=range j.PriorOwned {
  path:=filepath.Join(entry.TargetRoot,entry.RelativePath)
  if unchanged[archivePathKey(path)]{continue}
  if entry.Method==deployMethodJunction {
   if isDirectoryJunction(path) {
    actual:=junctionTarget(path)
    if !samePath(actual,entry.SourcePath){return result,fmt.Errorf("junction target changed during preparation: %s",path)}
    if err:=removeDirectoryJunction(path);err!=nil{return result,fmt.Errorf("remove retired junction %s: %w",path,err)}
   }
   continue
  }
  identity,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return result,err}
  if !exists{continue}
  if !sameArchiveObject(identity,entry.TargetIdentity){return result,fmt.Errorf("owned archive changed during preparation: %s",path)}
  backup:=filepath.Join(j.PreviousDir,entry.ID+stagingExtension)
  parked[archivePathKey(path)]=backup
  j.MovedFiles=append(j.MovedFiles,movedFileRecord{From:path,To:backup,SourceIdentity:identity})
 }
 for _,entry:=range result.Entries {
  if entry.Method==deployMethodJunction{continue} // junctions are created directly, not moved
  if unchanged[archivePathKey(entry.DestinationPath)]{continue}
  source:=stagePaths[archivePathKey(entry.DestinationPath)]
  if entry.Reuse {
   oldPath:=entry.ReusePath;if oldPath==""{oldPath=entry.DestinationPath}
   source=parked[archivePathKey(oldPath)]
  }
  if source==""{return result,fmt.Errorf("no staged archive for %s",entry.DestinationPath)}
  j.MovedFiles=append(j.MovedFiles,movedFileRecord{From:source,To:entry.DestinationPath,SourceIdentity:entry.TargetIdentity})
 }
 j.State=journalStatePrepared
 if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err}
 if beforeActivate!=nil{if err:=beforeActivate();err!=nil{return result,err}}
 for _,entry:=range plan.Entries {
  if entry.Method==deployMethodJunction{continue} // folder sources have no file identity
  current,err:=inspectArchiveFile(entry.SourcePath);if err!=nil{return result,err}
  if !sameArchiveObject(current,entry.SourceIdentity){return result,fmt.Errorf("source changed during preparation: %s",entry.SourcePath)}
 }
 for i,entry:=range result.Entries{
  if plan.Entries[i].SHA256=="" && entry.SHA256!=""{
   if err:=service.store.SetEntityArtifactSHA(ctx,entry.EntityID,entry.SHA256);err!=nil{return result,fmt.Errorf("persist verified archive checksum: %w",err)}
  }
 }
 // Play uses an isolated profile; only the marker backup is needed for recovery.
 if plan.Purpose==archivePurposePlay {
  j.MarkerBackup,j.MarkerExisted,err=readArchiveNativeBackup(playRuntimeMarkerPath(service.config));if err!=nil{return result,err}
 }
 j.State=journalStateActivating
 if err:=service.store.writeDeploymentJournal(ctx,j);err!=nil{return result,err}
 for _,move:=range j.MovedFiles {
  if err:=ctx.Err();err!=nil{return result,err}
  if err:=service.requireGameStopped();err!=nil{return result,err}
  if err:=executeArchiveMove(move);err!=nil{return result,err}
 }
 // Create new directory junctions after regular moves are complete.
 for _,entry:=range result.Entries {
  if entry.Method!=deployMethodJunction || entry.Reuse{continue}
  if err:=os.MkdirAll(filepath.Dir(entry.DestinationPath),0755);err!=nil{return result,err}
  if err:=ensureJunction(entry.DestinationPath,entry.SourcePath);err!=nil{return result,fmt.Errorf("create junction %s -> %s: %w",entry.DestinationPath,entry.SourcePath,err)}
 }
 if commit!=nil{if err:=commit();err!=nil{return result,err}}
 if err:=service.store.commitDeploymentApplied(ctx,j.ID,newOwned);err!=nil{return result,err}
 applied=true;j.State=journalStateApplied;j.AppliedEntries=newOwned
 cleanupCtx,cancel:=context.WithTimeout(context.WithoutCancel(ctx),30*time.Second);defer cancel()
 removed,cleanupErr:=service.finishArchiveJournal(cleanupCtx,j)
 result.Removed=removed
 if cleanupErr!=nil {
  service.emitStorageProgress(StorageProgress{OperationID:operationID,Phase:"cleanup-pending",Done:true,Error:cleanupErr.Error()})
  if err:=service.store.AppendEvent(cleanupCtx,"","archive_cleanup_pending",map[string]any{"operationId":operationID,"error":cleanupErr.Error()});err!=nil{service.emitStorageProgress(StorageProgress{OperationID:operationID,Phase:"cleanup-pending",Done:true,Error:errors.Join(cleanupErr,err).Error()})}
 }
 return result,nil
}

func copyArchivePayload(ctx context.Context,source string,destination *os.File,expected string,onProgress func(int64))(int64,string,error){
 input,err:=os.Open(source);if err!=nil{return 0,"",err};defer input.Close()
 hash:=sha256.New();buffer:=make([]byte,256*1024);var total int64
 lastProgress:=time.Now()
 for {
  if err:=ctx.Err();err!=nil{return total,"",err}
  n,readErr:=input.Read(buffer)
  if n>0 {written,err:=destination.Write(buffer[:n]);total+=int64(written);if err!=nil{return total,"",err};if written!=n{return total,"",io.ErrShortWrite};hash.Write(buffer[:n])}
  if onProgress!=nil && time.Since(lastProgress)>=250*time.Millisecond{onProgress(total);lastProgress=time.Now()}
  if readErr==io.EOF{break};if readErr!=nil{return total,"",readErr}
 }
 checksum:=hex.EncodeToString(hash.Sum(nil))
 if expected!="" && !strings.EqualFold(checksum,expected){return total,"",fmt.Errorf("archive checksum changed: %s",source)}
 if err:=destination.Sync();err!=nil{return total,"",err}
 return total,checksum,nil
}

func archivePathKey(path string)string{return strings.ToLower(filepath.Clean(path))}
func sameArchiveFileID(left,right ArchiveFileIdentity)bool{return left.IdentityKnown && right.IdentityKnown && left.VolumeID==right.VolumeID && left.FileID==right.FileID}
func archiveIdentityIfPresent(path string)(ArchiveFileIdentity,bool,error){
 info,err:=os.Lstat(path);if errors.Is(err,os.ErrNotExist){return ArchiveFileIdentity{},false,nil};if err!=nil{return ArchiveFileIdentity{},false,err}
 if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink!=0{return ArchiveFileIdentity{},true,fmt.Errorf("preserved non-regular or reparse entry: %s",path)}
 identity,err:=inspectArchiveFile(path);return identity,true,err
}
func executeArchiveMove(move movedFileRecord)error{
 current,exists,err:=archiveIdentityIfPresent(move.From);if err!=nil{return err}
 if !exists || !sameArchiveObject(current,move.SourceIdentity){return fmt.Errorf("move source changed: %s",move.From)}
 if _,exists,err:=archiveIdentityIfPresent(move.To);err!=nil{return err}else if exists{return fmt.Errorf("refuse to overwrite %s",move.To)}
 return renameArchiveNoReplace(move.From,move.To)
}
func validateArchiveChild(path,root string)error{
 if root=="" || samePath(path,root) || !pathWithin(path,root){return fmt.Errorf("archive path escapes its owned root: %s",path)}
 if info,err:=os.Lstat(root);err==nil {
  if !info.IsDir() || info.Mode()&os.ModeSymlink!=0{return fmt.Errorf("preserved reparse or non-directory root: %s",root)}
 } else if !errors.Is(err,os.ErrNotExist){return err}
 relative,err:=filepath.Rel(root,filepath.Dir(path));if err!=nil{return err}
 current:=root
 for _,part:=range strings.Split(relative,string(filepath.Separator)){
  if part=="." || part==""{continue};current=filepath.Join(current,part)
  info,err:=os.Lstat(current);if errors.Is(err,os.ErrNotExist){continue};if err!=nil{return err}
  if !info.IsDir() || info.Mode()&os.ModeSymlink!=0{return fmt.Errorf("preserved changed parent path: %s",current)}
 }
 return nil
}
func(service *AppService)archiveTransactionWorkRoot(purpose string)string{
 if purpose==archivePurposePlay{
  playRoot,err:=playUserPath(service.config)
  if err!=nil{return deploymentWorkDir(service.config.ActiveModsDir)}
  return playProfileDeploymentDir(playRoot)
 }
 return filepath.Join(service.config.ExportDir,".beamworlds-deployment")
}
func(service *AppService)playManagedRoot()string{
 playRoot,err:=playUserPath(service.config)
 if err!=nil{return filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)}
 return playProfileModsDir(playRoot)
}
func(service *AppService)validateOwnedArchivePath(entry OwnedArchiveEntry)error{
 if entry.ID=="" || filepath.Base(entry.ID)!=entry.ID || entry.OwnerID=="" || filepath.Base(entry.OwnerID)!=entry.OwnerID{return errors.New("invalid deployment ownership identity")}
 if entry.Method==deployMethodJunction {
  // Junction relative paths include the "unpacked" parent, e.g. "unpacked/mymod-id".
  if entry.RelativePath=="" || !pathWithin(filepath.Join(entry.TargetRoot,entry.RelativePath),entry.TargetRoot){return errors.New("invalid owned junction relative path")}
 } else {
  if entry.RelativePath=="" || filepath.Base(entry.RelativePath)!=entry.RelativePath{return errors.New("invalid owned archive relative path")}
 }
 if entry.Purpose==archivePurposeCollection {return validateArchiveChild(filepath.Join(entry.TargetRoot,entry.RelativePath),filepath.Join(service.config.ExportDir,collectionFolderDirectory,entry.OwnerID))}
 if entry.Purpose!=archivePurposePlay{return fmt.Errorf("unsupported deployment purpose %q",entry.Purpose)}
 // Accept both the profile mods path and the legacy managed dir.
 path:=filepath.Join(entry.TargetRoot,entry.RelativePath)
 root:=service.playManagedRoot()
 if err:=validateArchiveChild(path,root);err==nil{return nil}
 legacyRoot:=filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)
 return validateArchiveChild(path,legacyRoot)
}
func(service *AppService)validateArchiveJournalPaths(j deploymentJournalEntry)error{
 if j.ID=="" || j.OperationID=="" || filepath.Base(j.OperationID)!=j.OperationID{return errors.New("invalid deployment operation ID")}
 for _,entry:=range j.PriorOwned {if err:=service.validateOwnedArchivePath(entry);err!=nil{return err}}
 for _,entry:=range j.PreparedEntries {if err:=service.validateOwnedArchivePath(entry);err!=nil{return err}}
 root:=service.playManagedRoot()
 if j.Purpose==archivePurposeCollection {root=filepath.Join(service.config.ExportDir,collectionFolderDirectory,j.OwnerID)} else if j.Purpose!=archivePurposePlay{return fmt.Errorf("unsupported deployment journal purpose %q",j.Purpose)}
 if !samePath(j.DestinationRoot,root) && !pathWithin(j.DestinationRoot,root){
  // Accept the legacy managed-dir path for recovery of pre-migration journals.
  legacyRoot:=filepath.Join(service.config.ActiveModsDir,managedModDirectoryName)
  if !samePath(j.DestinationRoot,legacyRoot) && !pathWithin(j.DestinationRoot,legacyRoot){return errors.New("deployment journal destination no longer matches configured ownership")}
 }
 work:=service.archiveTransactionWorkRoot(j.Purpose)
 // Also accept the legacy work root for recovery.
 legacyWork:=deploymentWorkDir(service.config.ActiveModsDir)
 matchesWork:=samePath(j.StagingDir,filepath.Join(work,"staging-"+j.OperationID)) && samePath(j.PreviousDir,filepath.Join(work,"previous-"+j.OperationID))
 matchesLegacy:=samePath(j.StagingDir,filepath.Join(legacyWork,"staging-"+j.OperationID)) && samePath(j.PreviousDir,filepath.Join(legacyWork,"previous-"+j.OperationID))
 if !matchesWork && !matchesLegacy{return errors.New("deployment journal staging paths do not match owned operation")}
 actualWork:=work
 if matchesLegacy && !matchesWork{actualWork=legacyWork}
 if err:=validateArchiveChild(filepath.Join(j.StagingDir,"entry"),actualWork);err!=nil{return err}
 return validateArchiveChild(filepath.Join(j.PreviousDir,"entry"),actualWork)
}
func readArchiveNativeBackup(path string)([]byte,bool,error){data,err:=os.ReadFile(path);if errors.Is(err,os.ErrNotExist){return nil,false,nil};return data,err==nil,err}
func restoreNativeBackup(path string,backup []byte,existed bool)error{
 if existed{return writeFileAtomic(path,backup,0644)}
 err:=os.Remove(path);if errors.Is(err,os.ErrNotExist){return nil};return err
}

// rollbackArchiveJournal is idempotent: a recorded rename may or may not have
// happened. It never overwrites another file or discards a failed reverse move.
func(service *AppService)rollbackArchiveJournal(ctx context.Context,j deploymentJournalEntry)error{
 if err:=service.validateArchiveJournalPaths(j);err!=nil{return err}
 if j.State==journalStateActivating {
  if err:=service.requireGameStopped();err!=nil{return err}
  // Undo any junctions created during partial activation.
  for _,entry:=range j.Plan.Entries {
   if entry.Method!=deployMethodJunction || entry.Reuse{continue}
   if isDirectoryJunction(entry.DestinationPath) && samePath(junctionTarget(entry.DestinationPath),filepath.Clean(entry.SourcePath)) {
    _ = removeDirectoryJunction(entry.DestinationPath)
   }
  }
  // Restore any junctions removed during parking.
  for _,entry:=range j.PriorOwned {
   if entry.Method!=deployMethodJunction{continue}
   path:=filepath.Join(entry.TargetRoot,entry.RelativePath)
   if !isDirectoryJunction(path) {
    _ = os.MkdirAll(filepath.Dir(path),0755)
    _ = ensureJunction(path,entry.SourcePath)
   }
  }
  for i:=len(j.MovedFiles)-1;i>=0;i-- {
   move:=j.MovedFiles[i]
   if err:=service.validateJournalMove(j,move);err!=nil{return err}
   from,fromExists,err:=archiveIdentityIfPresent(move.From);if err!=nil{return err}
   to,toExists,err:=archiveIdentityIfPresent(move.To);if err!=nil{return err}
   if fromExists && sameArchiveObject(from,move.SourceIdentity){
    if toExists && sameArchiveObject(to,move.SourceIdentity){if err:=os.Remove(move.To);err!=nil{return err}}
    continue
   }
   if !toExists {
    // A later move in a chain was never reached; its source may still be
    // at the first move's original path. Reverse that first move below.
    found:=false
    for _,earlier:=range j.MovedFiles[:i]{if sameArchiveObject(earlier.SourceIdentity,move.SourceIdentity){identity,exists,err:=archiveIdentityIfPresent(earlier.From);if err!=nil{return err};if exists && sameArchiveObject(identity,move.SourceIdentity){found=true;break}}}
    if found{continue}
    return fmt.Errorf("neither journaled location contains recoverable archive: %s",move.From)
   }
   if !sameArchiveObject(to,move.SourceIdentity) || fromExists{return fmt.Errorf("preserved conflicting archive during recovery: %s",move.To)}
   if err:=renameArchiveNoReplace(move.To,move.From);err!=nil{return err}
  }
  if j.Purpose==archivePurposePlay {
   if err:=restoreNativeBackup(playRuntimeMarkerPath(service.config),j.MarkerBackup,j.MarkerExisted);err!=nil{return err}
  }
 }
 for _,entry:=range j.PreparedEntries {
  if entry.Method==deployMethodJunction{continue} // no staged files for junctions
  path:=filepath.Join(j.StagingDir,entry.ID+stagingExtension)
  current,exists,err:=archiveIdentityIfPresent(path);if err!=nil{return err};if !exists{continue}
  if !sameArchiveFileID(current,entry.TargetIdentity){return fmt.Errorf("preserved changed staging file: %s",path)}
  if err:=os.Remove(path);err!=nil{return err}
 }
 for _,dir:=range []string{j.StagingDir,j.PreviousDir}{if err:=removeEmptyArchiveDirectory(dir);err!=nil{return err}}
 return service.store.completeDeploymentJournal(ctx,j.ID,journalStateFailed,"interrupted deployment rolled back; previous selection preserved")
}
func(service *AppService)validateJournalMove(j deploymentJournalEntry,move movedFileRecord)error{
 allowed:=func(path string)bool{
  if pathWithin(path,j.StagingDir) || pathWithin(path,j.PreviousDir){return true}
  for _,entry:=range append(append([]OwnedArchiveEntry{},j.PriorOwned...),j.PreparedEntries...){if samePath(path,filepath.Join(entry.TargetRoot,entry.RelativePath)){return service.validateOwnedArchivePath(entry)==nil}}
  for _,entry:=range j.Plan.Entries{if samePath(path,entry.DestinationPath){return validateArchiveChild(path,j.DestinationRoot)==nil}}
  return false
 }
 if !allowed(move.From) || !allowed(move.To){return errors.New("journal move escapes recorded owned files")};return nil
}
func removeEmptyArchiveDirectory(path string)error{
 entries,err:=os.ReadDir(path);if errors.Is(err,os.ErrNotExist){return nil};if err!=nil{return err}
 if len(entries)>0{return fmt.Errorf("retained unresolved files in %s",path)}
 return os.Remove(path)
}

func(service *AppService)finishArchiveJournal(ctx context.Context,j deploymentJournalEntry)(int,error){
 if err:=service.validateArchiveJournalPaths(j);err!=nil{return 0,err}
 if err:=service.store.updateDeploymentJournalState(ctx,j.ID,journalStateCleanup);err!=nil{return 0,err}
 removed:=0
 for _,entry:=range j.PriorOwned {
  if entry.Method==deployMethodJunction{continue} // junctions removed during activation, no backup
  backup:=filepath.Join(j.PreviousDir,entry.ID+stagingExtension)
  current,exists,err:=archiveIdentityIfPresent(backup);if err!=nil{return removed,err};if !exists{continue}
  if !sameArchiveObject(current,entry.TargetIdentity){return removed,fmt.Errorf("preserved changed previous archive: %s",backup)}
  source,sourceExists,err:=archiveIdentityIfPresent(entry.SourcePath);if err!=nil{return removed,err}
  backed:=sourceExists && (sameArchiveObject(source,entry.SourceIdentity) || sameArchiveFileID(source,current))
  if !backed && sourceExists && entry.SHA256!="" {
   checksum,err:=hashFileSHA256(ctx,entry.SourcePath);if err!=nil{return removed,err}
   after,err:=inspectArchiveFile(entry.SourcePath);if err!=nil{return removed,err}
   backed=sameArchiveObject(source,after) && strings.EqualFold(checksum,entry.SHA256)
  }
  if !backed{backed=service.parkedArchiveLinkedElsewhere(backup,j)}
  if !backed{
   // The parked file is the last copy of these bytes. Keep it in the
   // library instead of leaving the cleanup unfinished for the user.
   if err:=service.keepLastArchiveCopyInLibrary(ctx,backup,entry);err!=nil{return removed,fmt.Errorf("keep the only remaining copy of %s: %w",backup,err)}
   continue
  }
  if err:=os.Remove(backup);err!=nil{return removed,err};removed++
 }
 for _,dir:=range []string{j.StagingDir,j.PreviousDir}{if err:=removeEmptyArchiveDirectory(dir);err!=nil{return removed,err}}
 service.store.writeMu.Lock();defer service.store.writeMu.Unlock()
 tx,err:=service.store.db.BeginTx(ctx,nil);if err!=nil{return removed,err};defer tx.Rollback()
 for _,entry:=range j.PriorOwned{if _,err:=tx.ExecContext(ctx,`DELETE FROM owned_archive_entries WHERE id=? AND state=?`,entry.ID,archiveStatePendingRetire);err!=nil{return removed,err}}
 if _,err:=tx.ExecContext(ctx,`UPDATE archive_deployment_journal SET state=?,completed_at=?,updated_at=? WHERE id=?`,journalStateDone,nowUTC(),nowUTC(),j.ID);err!=nil{return removed,err}
 return removed,tx.Commit()
}

// parkedArchiveLinkedElsewhere reports whether another hard link outside this
// deployment's work folders still names the parked file, for example a saved
// mod version. Removing the parked link then loses no data.
func(service *AppService)parkedArchiveLinkedElsewhere(backup string,j deploymentJournalEntry)bool{
 others,err:=otherHardLinkPaths(backup);if err!=nil{return false}
 work:=service.archiveTransactionWorkRoot(j.Purpose)
 for _,other:=range others{
  if pathWithin(other,j.StagingDir)||pathWithin(other,j.PreviousDir)||pathWithin(other,work){continue}
  return true
 }
 return false
}

// keepLastArchiveCopyInLibrary moves the only remaining copy of a previously
// deployed archive into the library's "Previous versions" folder. Studio placed
// it there, so it is indexed without asking the user to sort it.
func(service *AppService)keepLastArchiveCopyInLibrary(ctx context.Context,backup string,entry OwnedArchiveEntry)error{
 if strings.TrimSpace(service.config.LibraryDir)==""{return errors.New("no library folder is configured")}
 dir:=filepath.Join(service.config.LibraryDir,"Previous versions")
 if err:=os.MkdirAll(dir,0o755);err!=nil{return err}
 name:=filepath.Base(entry.SourcePath);if !isModArchive(name){name=entry.RelativePath}
 dest:=uniqueFilePath(dir,name)
 if err:=service.store.recordStudioPlacedArchive(ctx,dest);err!=nil{return err}
 if err:=moveFileVerified(ctx,service,backup,dest);err!=nil{return err}
 return service.store.AppendEvent(ctx,entry.EntityID,"archive_kept_in_library",map[string]any{"from":backup,"path":dest})
}

// Caller holds modImportMu (or runs before serving requests). Cleanup-only
// failures do not prevent an unrelated, already-canonical selection from use.
func(service *AppService)recoverArchiveDeployment(ctx context.Context)error{
 journals,err:=service.store.listPendingDeploymentJournals(ctx);if err!=nil{return err}
 for _,j:=range journals {
  if err:=ctx.Err();err!=nil{return err}
  if j.Purpose!=archivePurposePlay && j.Purpose!=archivePurposeCollection {continue}
  switch j.State {
  case journalStatePlanned,journalStatePrepared,journalStateActivating:
   if err:=service.rollbackArchiveJournal(ctx,j);err!=nil{return fmt.Errorf("recover deployment %s: %w",j.OperationID,err)}
  case journalStateApplied,journalStateCleanup:
   if _,err:=service.finishArchiveJournal(ctx,j);err!=nil{service.emitStorageProgress(StorageProgress{OperationID:j.OperationID,Phase:"cleanup-pending",Done:true,Error:err.Error()})}
  default:return fmt.Errorf("unknown deployment journal state %q",j.State)
  }
 }
 return service.store.compactCompletedJournals(ctx)
}

func (s *Store) savePreparedArchiveEntry(ctx context.Context,journalID string,entry OwnedArchiveEntry)error{
 payload,err:=json.Marshal(entry);if err!=nil{return err}
 s.writeMu.Lock();defer s.writeMu.Unlock()
 _,err=s.db.ExecContext(ctx,`INSERT INTO archive_deployment_prepared(journal_id,entry_id,entry_json) VALUES(?,?,?) ON CONFLICT(journal_id,entry_id) DO UPDATE SET entry_json=excluded.entry_json`,journalID,entry.ID,string(payload))
 return err
}
func (s *Store) loadPreparedArchiveEntries(ctx context.Context,journalID string)([]OwnedArchiveEntry,error){
 rows,err:=s.db.QueryContext(ctx,`SELECT entry_json FROM archive_deployment_prepared WHERE journal_id=? ORDER BY entry_id`,journalID);if err!=nil{return nil,err};defer rows.Close()
 var entries []OwnedArchiveEntry
 for rows.Next(){var payload string;if err:=rows.Scan(&payload);err!=nil{return nil,err};var entry OwnedArchiveEntry;if err:=json.Unmarshal([]byte(payload),&entry);err!=nil{return nil,err};entries=append(entries,entry)}
 return entries,rows.Err()
}
